package utils

import (
	"bytes"
	"golang.org/x/net/html/charset"
	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/encoding/traditionalchinese"
	"golang.org/x/text/encoding/unicode"
	"golang.org/x/text/transform"
	"html"
	"io"
	"regexp"
	"strings"
)

var DefaultUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"

// charsetPatternInDOMStr meta[http-equiv]元素, content属性中charset截取的正则模式.
// 如<meta http-equiv="content-type" content="text/html; charset=utf-8">
var charsetPatternInDOMStr = `charset\s*=\s*(\S*)\s*;?`

// charsetPattern 普通的MatchString可直接接受模式字符串, 无需Compile,
// 但是只能作为判断是否匹配, 无法从中获取其他信息.
var charsetPattern = regexp.MustCompile(charsetPatternInDOMStr)

// CharsetMap 字符集映射
var CharsetMap = map[string]encoding.Encoding{
	"utf-8":   unicode.UTF8,
	"gbk":     simplifiedchinese.GBK,
	"gb2312":  simplifiedchinese.GB18030,
	"gb18030": simplifiedchinese.GB18030,
	"big5":    traditionalchinese.Big5,
}

// HTMLCharacterEntitiesMap HTML 字符实体
var HTMLCharacterEntitiesMap = map[string]string{
	"\u00a0": "&nbsp;",
	"©":      "&copy;",
	"®":      "&reg;",
	"™":      "&trade;",
	"￠":      "&cent;",
	"£":      "&pound;",
	"¥":      "&yen;",
	"€":      "&euro;",
	"§":      "&sect;",
}

// DecodeToUTF8 从输入的byte数组中按照指定的字符集解析出对应的utf8格式的内容并返回.
func DecodeToUTF8(input []byte, charset encoding.Encoding) (output []byte, err error) {
	if charset == nil || charset == unicode.UTF8 {
		output = input
		return
	}
	reader := transform.NewReader(bytes.NewReader(input), charset.NewDecoder())
	output, err = io.ReadAll(reader)
	if err != nil {
		return
	}
	return
}

// EncodeFromUTF8 将输入的utf-8格式的byte数组中按照指定的字符集编码并返回
func EncodeFromUTF8(input []byte, charset encoding.Encoding) (output []byte, err error) {
	if charset == nil || charset == unicode.UTF8 {
		output = input
		return
	}
	reader := transform.NewReader(bytes.NewReader(input), encoding.ReplaceUnsupported(charset.NewEncoder()))
	output, err = io.ReadAll(reader)
	if err != nil {
		return
	}
	return
}

// GetPageCharset 解析页面, 从中获取页面编码信息
func GetPageCharset(content, contentType string) (charSet string, err error) {
	//log.Println("服务器返回编码：", contentType)
	if contentType != "" {
		matchedArray := charsetPattern.FindStringSubmatch(strings.ToLower(contentType))
		if len(matchedArray) > 1 {
			for _, matchedItem := range matchedArray[1:] {
				if strings.ToLower(matchedItem) != "utf-8" {
					charSet = matchedItem
					return
				}
			}
		}
	}
	//log.Println("继续查找编码1")
	var checkType string
	reg := regexp.MustCompile(`(?is)<title[^>]*>(.*?)<\/title>`)
	match := reg.FindStringSubmatch(content)
	if len(match) > 1 {
		_, checkType, _ = charset.DetermineEncoding([]byte(match[1]), "")
		//log.Println("Title解析编码：", checkType)
		if checkType == "utf-8" {
			charSet = checkType
			return
		}
	}
	//log.Println("继续查找编码2")
	reg = regexp.MustCompile(`(?is)<meta[^>]*charset\s*=["']?\s*([\w\d\-]+)`)
	match = reg.FindStringSubmatch(content)
	if len(match) > 1 {
		charSet = match[1]
		return
	}
	//log.Println("找不到编码")
	charSet = "utf-8"
	return
}

// ReplaceHTMLCharacterEntities 替换页面中html实体字符, 以免写入文件时遇到不支持的字符
func ReplaceHTMLCharacterEntities(input string, charset encoding.Encoding) (output string) {
	if charset == nil || charset == unicode.UTF8 {
		output = input
		return
	}
	output = html.UnescapeString(input)
	for char, entity := range HTMLCharacterEntitiesMap {
		output = strings.Replace(output, char, entity, -1)
	}
	return
}
