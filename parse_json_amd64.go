//go:build !noasm && !appengine && gc
// +build !noasm,!appengine,gc

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
	"bytes"
	"errors"
	"sync"
)

func (pj *internalParsedJson) initialize(size int) {
	// 估算带子的大小，大约为 JSON 消息长度的 15%
	avgTapeSize := size * 15 / 100
	// 如果当前带子的容量小于估算的大小，则重新分配带子
	if cap(pj.Tape) < avgTapeSize {
		pj.Tape = make([]uint64, 0, avgTapeSize) // 创建新的带子
	}
	pj.Tape = pj.Tape[:0] // 清空带子

	// 计算字符串缓冲区的大小，默认为消息长度的 10%
	stringsSize := size / 10
	if stringsSize < 128 {
		stringsSize = 128 // 始终至少为字符串缓冲区分配 128 字节
	}
	// 如果已有字符串缓冲区且其容量足够，则清空缓冲区
	if pj.Strings != nil && cap(pj.Strings.B) >= stringsSize {
		pj.Strings.B = pj.Strings.B[:0]
	} else {
		// 否则，创建新的字符串缓冲区
		pj.Strings = &TStrings{make([]byte, 0, stringsSize)}
	}
	// 如果当前的作用域偏移量容量小于最大深度，则重新分配
	if cap(pj.containingScopeOffset) < maxdepth {
		pj.containingScopeOffset = make([]uint64, 0, maxdepth) // 创建新的作用域偏移量
	}
	pj.containingScopeOffset = pj.containingScopeOffset[:0] // 清空作用域偏移量
	pj.indexesChan = indexChan{}                            // 初始化索引通道
}

func (pj *internalParsedJson) parseMessage(msg []byte, ndjson bool) (err error) {
	// 缓存消息，以便可以直接指向字符串
	// TODO: 找出为什么 TestVerifyTape/instruments 在没有 bytes.TrimSpace 的情况下会失败
	pj.Message = bytes.TrimSpace(msg) // 去除消息首尾空白
	pj.initialize(len(pj.Message))    // 初始化解析器，传入消息长度

	// 根据是否为 NDJSON 设置标志
	if ndjson {
		pj.ndjson = 1
	} else {
		pj.ndjson = 0
	}

	// 将通道的容量设置为小于槽位的数量。
	// 这样发送者会自动阻塞，直到消费者完成正在处理的槽位。
	if pj.indexChans == nil {
		pj.indexChans = make(chan indexChan, indexSlots-2) // 创建通道
	}
	pj.buffersOffset = ^uint64(0) // 设置缓冲区偏移量为最大值

	var errStage1 error

	// 对于较长的输入，异步处理
	if len(pj.Message) > 8<<10 {
		var wg sync.WaitGroup
		wg.Add(1) // 增加等待组计数
		go func() {
			defer wg.Done() // 完成时减少计数
			if ok, done := pj.unifiedMachine(); !ok {
				err = errors.New("Bad parsing while executing stage 2") // 解析错误
				// 继续消费...
				if !done {
					for idx := range pj.indexChans {
						if idx.index == -1 {
							break // 如果索引为 -1，退出循环
						}
					}
				}
			}
		}()
		if !pj.findStructuralIndices() {
			errStage1 = errors.New("Failed to find all structural indices for stage 1") // 找不到结构索引
		}
		wg.Wait() // 等待所有 goroutine 完成
	} else {
		if !pj.findStructuralIndices() {
			// 清空通道直到为空
			for idx := range pj.indexChans {
				if idx.index == -1 {
					break // 如果索引为 -1，退出循环
				}
			}
			return errors.New("Failed to find all structural indices for stage 1") // 找不到结构索引
		}
		if ok, _ := pj.unifiedMachine(); !ok {
			// 清空通道直到为空
			for {
				select {
				case idx := <-pj.indexChans:
					if idx.index == -1 {
						return errors.New("Bad parsing while executing stage 2") // 解析错误
					}
					// 已经清空。
				default:
					return errors.New("Bad parsing while executing stage 2") // 解析错误
				}
			}
		}
		return nil // 解析成功
	}

	if errStage1 != nil {
		return errStage1 // 返回阶段 1 的错误
	}
	return // 返回 nil，表示没有错误
}
