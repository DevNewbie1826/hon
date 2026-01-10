package parser

import (
	"bytes"
	"strconv"
)

var (
	headerEnd  = []byte("\r\n\r\n")
	headerCL   = []byte("Content-Length:")
	headerTE   = []byte("Transfer-Encoding:")
	valChunked = []byte("chunked")
	chunkEnd   = []byte("0\r\n\r\n")
)

type CheckResult struct {
	Complete      bool  // 요청이 완전히 수신되었는가?
	BytesConsumed int   // 요청 전체의 길이 (Header + Body)
	Error         error
}

func CheckRequest(data []byte) CheckResult {
	// 1. 헤더 경계 검색
	headerEndIdx := bytes.Index(data, headerEnd)
	if headerEndIdx == -1 {
		return CheckResult{Complete: false}
	}

	headerBodySep := headerEndIdx + 4
	headers := data[:headerEndIdx]

	contentLength := -1
	isChunked := false

	// 2. 주요 헤더 스캔 (Content-Length / Transfer-Encoding)
	lines := bytes.Split(headers, []byte("\r\n"))
	if len(lines) > 1 {
		for _, line := range lines[1:] {
			// Content-Length 확인
			if len(line) > len(headerCL) && bytes.EqualFold(line[:len(headerCL)], headerCL) {
				val := bytes.TrimSpace(line[len(headerCL):])
				if cl, err := strconv.Atoi(string(val)); err == nil {
					contentLength = cl
				}
				continue
			}
			// Transfer-Encoding 확인
			if len(line) > len(headerTE) && bytes.EqualFold(line[:len(headerTE)], headerTE) {
				val := bytes.TrimSpace(line[len(headerTE):])
				if bytes.Contains(bytes.ToLower(val), valChunked) {
					isChunked = true
				}
			}
		}
	}

	// 3. 바디 완성 여부 판단
	if isChunked {
		bodyData := data[headerBodySep:]
		chunkEndIdx := bytes.Index(bodyData, chunkEnd)
		if chunkEndIdx != -1 {
			return CheckResult{Complete: true, BytesConsumed: headerBodySep + chunkEndIdx + 5}
		}
		return CheckResult{Complete: false}
	}

	if contentLength >= 0 {
		totalLen := headerBodySep + contentLength
		if len(data) >= totalLen {
			return CheckResult{Complete: true, BytesConsumed: totalLen}
		}
		return CheckResult{Complete: false}
	}

	// 바디가 없는 요청 (GET, HEAD 등)
	return CheckResult{Complete: true, BytesConsumed: headerBodySep}
}
