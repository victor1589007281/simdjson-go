//go:build !appengine && !noasm && gc
// +build !appengine,!noasm,gc

/*
 * MinIO Cloud Storage, (C) 2020 MinIO, Inc.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package simdjson

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"runtime"
	"sync"

	"github.com/klauspost/cpuid/v2"
)

var wantFeatures = cpuid.CombineFeatures(cpuid.AVX2, cpuid.CLMUL) // 组合所需的 CPU 特性，包括 AVX2 和 CLMUL

// SupportedCPU 将返回当前 CPU 是否支持所需的特性。
func SupportedCPU() bool {
	return cpuid.CPU.HasAll(wantFeatures) // 检查 CPU 是否具备所有所需特性
}

func newInternalParsedJson(reuse *ParsedJson, opts []ParserOption) (*internalParsedJson, error) {
	// 检查主机 CPU 是否支持所需的特性
	if !SupportedCPU() {
		return nil, errors.New("Host CPU does not meet target specs") // 如果不支持，返回错误
	}
	var pj *internalParsedJson
	// 如果提供了可重用的 ParsedJson，并且其内部对象不为 nil
	if reuse != nil && reuse.internal != nil {
		pj = reuse.internal          // 使用重用的内部解析 JSON 对象
		pj.ParsedJson = *reuse       // 复制重用的 ParsedJson 数据
		pj.ParsedJson.internal = nil // 清空重用对象的内部引用
		reuse = &ParsedJson{}        // 创建一个新的 ParsedJson 对象以供重用
	}
	// 如果没有重用的对象，则创建一个新的内部解析 JSON 对象
	if pj == nil {
		pj = &internalParsedJson{}
	}
	pj.copyStrings = true // 设置复制字符串的标志
	// 应用所有提供的解析选项
	for _, opt := range opts {
		if err := opt(pj); err != nil {
			return nil, err // 如果应用选项时出错，返回错误
		}
	}
	return pj, nil // 返回创建的内部解析 JSON 对象
}

// Parse 从数据块中解析一个对象或数组并返回解析后的 JSON。
// 可以提供一个可选的之前解析的 JSON 块以减少内存分配。
func Parse(b []byte, reuse *ParsedJson, opts ...ParserOption) (*ParsedJson, error) {
	// 创建一个新的内部解析 JSON 对象
	pj, err := newInternalParsedJson(reuse, opts)
	if err != nil {
		return nil, err // 如果创建失败，返回错误
	}

	// 解析消息，指示这是一个普通的 JSON 而不是换行分隔的 JSON
	err = pj.parseMessage(b, false)
	if err != nil {
		return nil, err // 如果解析失败，返回错误
	}

	// 获取解析后的 JSON 对象，并将内部引用设置为当前解析对象
	parsed := &pj.ParsedJson
	parsed.internal = pj
	return parsed, nil // 返回解析后的 JSON 对象
}

// ParseND will parse newline delimited JSON objects or arrays.
// An optional block of previously parsed json can be supplied to reduce allocations.
func ParseND(b []byte, reuse *ParsedJson, opts ...ParserOption) (*ParsedJson, error) {
	// 创建一个新的内部解析 JSON 对象
	pj, err := newInternalParsedJson(reuse, opts)
	if err != nil {
		return nil, err // 如果创建失败，返回错误
	}

	// 解析消息，去除首尾空白，并指示这是一个换行分隔的 JSON
	err = pj.parseMessage(bytes.TrimSpace(b), true)
	if err != nil {
		return nil, err // 如果解析失败，返回错误
	}

	// 返回解析后的 JSON 对象
	return &pj.ParsedJson, nil
}

// A Stream is used to stream back results.
// Either Error or Value will be set on returned results.
type Stream struct {
	Value *ParsedJson
	Error error
}

// ParseNDStream 将解析一个流并将解析后的 JSON 返回到提供的结果通道。
// 该方法将立即返回。
// 每个元素都包含在一个根标签内。
//
//	<root>Element 1</root><root>Element 2</root>...
//
// 每个结果将包含不确定数量的完整元素，
// 因此可以假设每个结果都以根标签开始和结束。
// 解析器将继续解析，直到写入结果流被阻塞。
// 当返回非 nil 错误时，流结束。
// 如果流解析到末尾，错误值将为 io.EOF
// 在返回错误后，通道将被关闭。
// 可以提供一个可选的通道以返回已消耗的结果。
// 没有保证元素会被消耗，因此始终使用
// 非阻塞写入到重用通道。
func ParseNDStream(r io.Reader, res chan<- Stream, reuse <-chan *ParsedJson) {
	// 检查主机 CPU 是否支持所需的特性
	if !SupportedCPU() {
		go func() {
			res <- Stream{
				Value: nil,
				Error: fmt.Errorf("Host CPU does not meet target specs"), // 如果不支持，返回错误
			}
			close(res) // 关闭结果通道
		}()
		return
	}
	const tmpSize = 10 << 20               // 定义临时缓冲区大小
	buf := bufio.NewReaderSize(r, tmpSize) // 创建带缓冲的读取器
	tmpPool := sync.Pool{New: func() interface{} {
		return make([]byte, tmpSize+1024) // 创建临时字节池
	}}
	conc := (runtime.GOMAXPROCS(0) + 1) / 2 // 计算并发数
	queue := make(chan chan Stream, conc)   // 创建通道以传递结果

	go func() {
		// 按顺序转发完成的项目
		defer close(res) // 结束时关闭结果通道
		end := false
		for items := range queue {
			i := <-items // 从队列中获取结果
			select {
			case res <- i: // 将结果发送到结果通道
			default:
				if !end {
					// 如果尚未返回错误，则阻塞
					res <- i
				}
			}
			if i.Error != nil {
				end = true // 如果有错误，结束处理
			}
		}
	}()

	go func() {
		defer close(queue) // 结束时关闭队列
		for {
			tmp := tmpPool.Get().([]byte) // 从临时池获取字节
			tmp = tmp[:tmpSize]           // 设置临时字节大小
			n, err := buf.Read(tmp)       // 从读取器中读取数据
			if err != nil && err != io.EOF {
				queueError(queue, err) // 处理错误
				return
			}
			tmp = tmp[:n] // 截取读取的字节
			// 读取直到换行符
			if err != io.EOF {
				b, err2 := buf.ReadBytes('\n') // 读取直到换行
				if err2 != nil && err2 != io.EOF {
					queueError(queue, err2) // 处理错误
					return
				}
				tmp = append(tmp, b...) // 将读取的字节追加到临时字节
				// 转发 io.EOF
				err = err2
			}

			if len(tmp) > 0 {
				result := make(chan Stream, 0) // 创建结果通道
				queue <- result                // 将结果通道放入队列
				go func() {
					var pj internalParsedJson
					pj.copyStrings = true // 设置复制字符串的标志
					select {
					case v := <-reuse: // 尝试从重用通道获取已解析的 JSON
						if cap(v.Message) >= tmpSize+1024 {
							tmpPool.Put(v.Message) // 如果容量足够，放回临时池
							v.Message = nil
						}
						pj.ParsedJson = *v // 复制重用的 ParsedJson

					default:
					}
					// 解析消息
					parseErr := pj.parseMessage(tmp, true)
					if parseErr != nil {
						result <- Stream{
							Value: nil,
							Error: fmt.Errorf("parsing input: %w", parseErr), // 返回解析错误
						}
						return
					}
					parsed := pj.ParsedJson
					result <- Stream{
						Value: &parsed, // 返回解析后的 JSON
						Error: nil,
					}
				}()
			} else {
				tmpPool.Put(tmp) // 如果没有数据，将临时字节放回池中
			}
			if err != nil {
				// 应该只是真正的 io.EOF
				queueError(queue, err) // 处理错误
				return
			}
		}
	}()
}

// queueError 将错误放入队列
func queueError(queue chan chan Stream, err error) {
	result := make(chan Stream, 0) // 创建结果通道
	queue <- result                // 将结果通道放入队列
	result <- Stream{
		Value: nil,
		Error: err, // 返回错误
	}
}
