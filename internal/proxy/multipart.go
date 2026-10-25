package proxy

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"mime"
	"net/textproto"
)

// 本文件处理 multipart 表单里唯一需要改写的东西：model 字段。
//
// 表单端点（图像编辑/变体、语音转写/翻译）的请求体对 AMKR 不透明，只从表单里取
// model 做**本地路由**，原始字节整份转发。这与 JSON 路径不一致——JSON 会把 model
// 换成选中 target 的上游模型名（proxysupport.UpstreamBody）。于是任务名、别名、
// `模型[Key]` 以及 upstream_model 与本地名不同的模型，在表单请求里会被原样送到上游：
// 接受本地名的上游能成，只认真实模型名的上游直接 400。实测线上图像编辑就是这一条
// （上游回 `Model image_generate is not supported`，而 AMKR 认为路由到了
// gpt-image-2.5-sunburst）。
//
// 修法是把同一份「上游模型名」也写回表单。**只改 model 的值**：其余字节、boundary、
// 图片/音频内容一个不动（见 multipartWithModel）。

// flatUpstreamBody 返回 multipart 请求实际要发给上游的字节。
//
// 表单里的 model 与本次要发的上游模型名相同时逐字节透传——既有契约（文件内容对
// AMKR 不透明、不做任何改写）在原样可达的情况下保持不变，也不做无谓的重建；不同时
// 才重建，且只替换 model 的值。
func flatUpstreamBody(context *RequestContext, upstreamModelID string) ([]byte, error) {
	requested := context.Payload.Lookup("model")
	if !requested.IsString() || requested.Str == "" || requested.Str == upstreamModelID {
		return context.OriginalRaw, nil
	}
	return multipartWithModel(context.Request.Header.Get("Content-Type"), context.OriginalRaw, upstreamModelID)
}

// multipartWithModel 返回把表单里 model 字段的值换成 model 之后的请求体。
//
// 用字节拼接而不是 multipart.Writer 重建：重建会把每个 part 的头按 Go 的格式重写
// 一遍（键序、参数引号都可能变），而这份请求体对 AMKR 是不透明的，除 model 之外
// 没有任何理由改动它——图片/音频字节必须逐字节保持，否则上游收到的是另一张图。
func multipartWithModel(contentType string, body []byte, model string) ([]byte, error) {
	_, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		return nil, fmt.Errorf("解析 Content-Type 失败: %w", err)
	}
	boundary := params["boundary"]
	if boundary == "" {
		return nil, errors.New("Content-Type 里没有 boundary")
	}
	start, end, found := multipartModelValueSpan(body, boundary)
	if !found {
		return nil, errors.New("表单里没有 model 字段")
	}
	rewritten := make([]byte, 0, len(body)-(end-start)+len(model))
	rewritten = append(rewritten, body[:start]...)
	rewritten = append(rewritten, model...)
	rewritten = append(rewritten, body[end:]...)
	return rewritten, nil
}

// multipartModelValueSpan 返回表单里 model 字段**值**的字节区间 [start, end)。
//
// 只扫分隔符与 part 头，不解析文件内容：图片/音频对 AMKR 不透明，没必要再解一遍。
// found=false 表示这份表单里没有 model 字段（调用方按「无法改写」处理，而不是把
// 一个错的模型名发给上游）。
func multipartModelValueSpan(body []byte, boundary string) (start, end int, found bool) {
	offsets := multipartBoundaryOffsets(body, []byte("--"+boundary))
	// 每两个相邻分隔符之间是一个 part；最后一个分隔符是结束标记或没有后继，跳过。
	for index := 0; index+1 < len(offsets); index++ {
		regionStart, ok := multipartPartRegionStart(body, offsets[index])
		if !ok {
			continue
		}
		regionEnd := offsets[index+1] - multipartLineBreakBefore(body, offsets[index+1])
		if regionEnd < regionStart {
			continue
		}
		contentStart, ok := multipartModelPartContentStart(body[regionStart:regionEnd])
		if !ok {
			continue
		}
		return regionStart + contentStart, regionEnd, true
	}
	return 0, 0, false
}

// multipartBoundaryOffsets 返回正文里每个分隔符 `--boundary` 的起始位置。
//
// 只认「正文开头或换行之后」的出现：正文里撞上同样字节的巧合不该被当成分隔符。
func multipartBoundaryOffsets(body, marker []byte) []int {
	offsets := make([]int, 0, 8)
	for from := 0; ; {
		index := bytes.Index(body[from:], marker)
		if index < 0 {
			return offsets
		}
		at := from + index
		if at == 0 || multipartLineBreakBefore(body, at) > 0 {
			offsets = append(offsets, at)
		}
		from = at + len(marker)
	}
}

// multipartLineBreakBefore 返回 pos 之前那个换行的长度（0 表示 pos 前面不是换行）。
//
// 同时认 CRLF 与裸 LF：mime/multipart 的读取器两者都接受，这里不能比它更严格。
func multipartLineBreakBefore(body []byte, pos int) int {
	switch {
	case pos >= 2 && body[pos-2] == '\r' && body[pos-1] == '\n':
		return 2
	case pos >= 1 && body[pos-1] == '\n':
		return 1
	}
	return 0
}

// multipartPartRegionStart 返回某个分隔符行之后「part 头部」的起始位置。
func multipartPartRegionStart(body []byte, delimiterStart int) (int, bool) {
	lineEnd := bytes.IndexByte(body[delimiterStart:], '\n')
	if lineEnd < 0 {
		return 0, false
	}
	return delimiterStart + lineEnd + 1, true
}

// multipartModelPartContentStart 判断这段 part（头部 + 内容）是不是 model 字段，
// 是则返回值的起始偏移（相对 part 开头）。
func multipartModelPartContentStart(region []byte) (int, bool) {
	headerEnd, contentStart := multipartHeaderEnd(region)
	if headerEnd < 0 {
		return 0, false
	}
	if !multipartIsModelPart(region[:headerEnd]) {
		return 0, false
	}
	return contentStart, true
}

// multipartHeaderEnd 返回 part 里头部与内容的交界：headerEnd 是头部末尾，
// contentStart 是内容起点。空头部的 part 不在本文件的处理范围内。
func multipartHeaderEnd(region []byte) (headerEnd, contentStart int) {
	if index := bytes.Index(region, []byte("\r\n\r\n")); index >= 0 {
		return index, index + 4
	}
	// 裸 LF 的 body（Go 的 multipart 读取器也接受）：
	if index := bytes.Index(region, []byte("\n\n")); index >= 0 {
		return index, index + 2
	}
	return -1, 0
}

// multipartIsModelPart 判断这段 part 头是不是表单字段 model。
func multipartIsModelPart(headers []byte) bool {
	// 补一个空行终止头部块：ReadMIMEHeader 在头部之后直接 EOF 时会带 io.EOF 返回，
	// 追加分隔空行让「解析成功」与「头部合法」是同一件事。
	block := make([]byte, 0, len(headers)+4)
	block = append(block, headers...)
	block = append(block, "\r\n\r\n"...)
	fields, err := textproto.NewReader(bufio.NewReader(bytes.NewReader(block))).ReadMIMEHeader()
	if err != nil {
		return false
	}
	_, params, err := mime.ParseMediaType(fields.Get("Content-Disposition"))
	if err != nil {
		return false
	}
	return params["name"] == "model"
}
