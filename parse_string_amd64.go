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
	"reflect"
	"unsafe"
)

//go:noescape
// _parse_string_validate_only 是一个汇编函数，用于验证字符串的有效性。
// 它接受源数据、最大字符串大小、字符串长度和目标长度的指针，并返回结果。
func _parse_string_validate_only(src, maxStringSize, str_length, dst_length unsafe.Pointer) (result uint64)

//go:noescape
// _parse_string 是一个汇编函数，用于解析字符串。
// 它接受源数据、目标缓冲区和当前字符串缓冲区位置的指针，并返回结果。
func _parse_string(src, dst, pcurrent_string_buf_loc unsafe.Pointer) (res uint64)

// parseStringSimdValidateOnly 验证给定字节缓冲区中的字符串是否有效。
// buf: 字节缓冲区，包含待验证的字符串。
// maxStringSize: 最大字符串大小的指针。
// dstLength: 目标字符串长度的指针。
// needCopy: 指示是否需要复制的指针。
func parseStringSimdValidateOnly(buf []byte, maxStringSize, dstLength *uint64, needCopy *bool) bool {
	src := unsafe.Pointer(&buf[1]) // 使用 buf[1] 跳过开头的引号
	src_length := uint64(0) // 初始化源字符串长度

	// 调用汇编函数进行字符串验证
	success := _parse_string_validate_only(src, unsafe.Pointer(&maxStringSize), unsafe.Pointer(&src_length), unsafe.Pointer(dstLength))

	// 检查是否需要复制字符串
	*needCopy = *needCopy || src_length != *dstLength
	return success != 0 // 返回验证结果
}

// parseStringSimd 解析给定字节缓冲区中的字符串，并将结果存储在提供的字符串缓冲区中。
// buf: 字节缓冲区，包含待解析的字符串。
// stringbuf: 指向字符串缓冲区的指针。
func parseStringSimd(buf []byte, stringbuf *[]byte) bool {
	sh := (*reflect.SliceHeader)(unsafe.Pointer(stringbuf)) // 获取字符串缓冲区的切片头
	sb := *stringbuf
	sb = append(sb, 0) // 在缓冲区末尾添加一个零字节

	src := unsafe.Pointer(&buf[1]) // 使用 buf[1] 跳过开头的引号
	string_buf_loc := unsafe.Pointer(uintptr(unsafe.Pointer(&sb[0])) + uintptr(sh.Len)*unsafe.Sizeof(sb[0])) // 计算目标位置
	dst := string_buf_loc // 设置目标位置

	// 调用汇编函数进行字符串解析
	res := _parse_string(src, dst, unsafe.Pointer(&string_buf_loc))

	// 更新字符串缓冲区的长度
	sh.Len += int(uintptr(string_buf_loc) - uintptr(dst))

	return res != 0 // 返回解析结果
}
