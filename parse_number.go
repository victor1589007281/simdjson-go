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
	"errors"
	"math"
	"reflect"
	"strconv"
	"unsafe"
)

const (
	isPartOfNumberFlag  = 1 << iota // 标记数字的一部分
	isFloatOnlyFlag                 // 标记仅为浮点数
	isMinusFlag                     // 标记负号
	isEOVFlag                       // 标记结束值
	isDigitFlag                     // 标记数字
	isMustHaveDigitNext             // 标记必须有下一个数字
)

// isNumberRune 数组用于标识每个字符是否为数字的一部分
var isNumberRune = [256]uint8{
	'0':  isPartOfNumberFlag | isDigitFlag,
	'1':  isPartOfNumberFlag | isDigitFlag,
	'2':  isPartOfNumberFlag | isDigitFlag,
	'3':  isPartOfNumberFlag | isDigitFlag,
	'4':  isPartOfNumberFlag | isDigitFlag,
	'5':  isPartOfNumberFlag | isDigitFlag,
	'6':  isPartOfNumberFlag | isDigitFlag,
	'7':  isPartOfNumberFlag | isDigitFlag,
	'8':  isPartOfNumberFlag | isDigitFlag,
	'9':  isPartOfNumberFlag | isDigitFlag,
	'.':  isPartOfNumberFlag | isFloatOnlyFlag | isMustHaveDigitNext,
	'+':  isPartOfNumberFlag,
	'-':  isPartOfNumberFlag | isMinusFlag | isMustHaveDigitNext,
	'e':  isPartOfNumberFlag | isFloatOnlyFlag,
	'E':  isPartOfNumberFlag | isFloatOnlyFlag,
	',':  isEOVFlag,
	'}':  isEOVFlag,
	']':  isEOVFlag,
	' ':  isEOVFlag,
	'\t': isEOVFlag,
	'\r': isEOVFlag,
	'\n': isEOVFlag,
	':':  isEOVFlag,
}

// parseNumber 将解析从缓冲区开始的数字。
// 任何非数字字符将被忽略。
// 如果未找到有效值，则返回 TagEnd。
func parseNumber(buf []byte) (id, val uint64) {
	pos := 0          // 当前解析位置
	found := uint8(0) // 标记找到的类型
	for i, v := range buf {
		t := isNumberRune[v] // 获取当前字符的标记
		if t == 0 {
			// 如果字符不是数字的一部分，返回 0
			return 0, 0
		}
		if t == isEOVFlag {
			break // 遇到结束标记，停止解析
		}
		if t&isMustHaveDigitNext > 0 {
			// 如果是小数点或负号，必须后面跟一个数字
			if len(buf) < i+2 || isNumberRune[buf[i+1]]&isDigitFlag == 0 {
				return 0, 0 // 如果没有跟随数字，返回 0
			}
		}
		found |= t  // 更新找到的标记
		pos = i + 1 // 更新当前解析位置
	}
	if pos == 0 {
		return 0, 0 // 如果没有找到有效数字，返回 0
	}
	const maxIntLen = 20                          // 最大整数长度
	floatTag := uint64(TagFloat) << JSONTAGOFFSET // 浮点数标记

	// 仅在未找到浮点数且可以适应整数时尝试解析整数
	if found&isFloatOnlyFlag == 0 && pos <= maxIntLen {
		if found&isMinusFlag == 0 {
			if pos > 1 && buf[0] == '0' {
				// 整数不能有前导零
				return 0, 0
			}
		} else {
			if pos > 2 && buf[1] == '0' {
				// 负数后面不能有前导零
				return 0, 0
			}
		}
		i64, err := strconv.ParseInt(unsafeBytesToString(buf[:pos]), 10, 64) // 尝试解析为整数
		if err == nil {
			return uint64(TagInteger) << JSONTAGOFFSET, uint64(i64) // 返回整数标记和值
		}
		if errors.Is(err, strconv.ErrRange) {
			floatTag |= uint64(FloatOverflowedInteger) // 标记为溢出整数
		}

		if found&isMinusFlag == 0 {
			u64, err := strconv.ParseUint(unsafeBytesToString(buf[:pos]), 10, 64) // 尝试解析为无符号整数
			if err == nil {
				return uint64(TagUint) << JSONTAGOFFSET, u64 // 返回无符号整数标记和值
			}
			if errors.Is(err, strconv.ErrRange) {
				floatTag |= uint64(FloatOverflowedInteger) // 标记为溢出整数
			}
		}
	} else if found&isFloatOnlyFlag == 0 {
		floatTag |= uint64(FloatOverflowedInteger) // 标记为溢出整数
	}

	if pos > 1 && buf[0] == '0' && isNumberRune[buf[1]]&isFloatOnlyFlag == 0 {
		// 浮点数只能在后面跟小数点时有前导零
		return 0, 0
	}
	f64, err := strconv.ParseFloat(unsafeBytesToString(buf[:pos]), 64) // 尝试解析为浮点数
	if err == nil {
		return floatTag, math.Float64bits(f64) // 返回浮点数标记和位表示
	}
	return 0, 0 // 如果解析失败，返回 0
}

// unsafeBytesToString 仅在我们控制 b 时使用。
func unsafeBytesToString(b []byte) (s string) {
	var length = len(b) // 获取字节数组的长度

	if length == 0 {
		return "" // 如果长度为 0，返回空字符串
	}

	// 使用反射将字节数组转换为字符串
	stringHeader := (*reflect.StringHeader)(unsafe.Pointer(&s))
	stringHeader.Data = uintptr(unsafe.Pointer(&b[0])) // 设置字符串数据指针
	stringHeader.Len = length                          // 设置字符串长度
	return s                                           // 返回字符串
}
