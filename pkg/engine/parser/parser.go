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
	// Zero-Alloc Iterator: Scan headers line by line
	// headers slice contains everything up to \r\n\r\n
	
	// Skip Request Line (First line)
	cur := headers
	if idx := bytes.Index(cur, []byte("\r\n")); idx != -1 {
		cur = cur[idx+2:]
	} else {
		// No CRLF in headers? Should not happen if headerEndIdx was found
		return CheckResult{Complete: false}
	}

	for len(cur) > 0 {
		var line []byte
		idx := bytes.Index(cur, []byte("\r\n"))
		if idx != -1 {
			line = cur[:idx]
			cur = cur[idx+2:]
		} else {
			// Last line (headers slice might exclude the final CRLF)
			line = cur
			cur = nil
		}

		// Check for Content-Length
		if len(line) > len(headerCL) && bytes.EqualFold(line[:len(headerCL)], headerCL) {
			// Parse value: Trim spaces
			val := line[len(headerCL):]
			val = bytes.TrimSpace(val)
			if cl, err := strconv.Atoi(string(val)); err == nil {
				contentLength = cl
			}
			continue
		}

		// Check for Transfer-Encoding
		if len(line) > len(headerTE) && bytes.EqualFold(line[:len(headerTE)], headerTE) {
			val := line[len(headerTE):]
			val = bytes.TrimSpace(val)
			if bytes.Contains(bytes.ToLower(val), valChunked) {
				isChunked = true
			}
		}
	}

	// 3. 바디 완성 여부 판단
	if isChunked {
		bodyData := data[headerBodySep:]
		offset := 0
		
		for {
			// Find CRLF at end of chunk size line
			idx := bytes.Index(bodyData[offset:], []byte("\r\n"))
			if idx == -1 {
				return CheckResult{Complete: false}
			}
			
			// Parse Chunk Size (hex)
			// Handle Chunk Extensions: Size is before first semicolon if present
			line := bodyData[offset : offset+idx]
			if semi := bytes.IndexByte(line, ';'); semi != -1 {
				line = line[:semi]
			}
			
			// Trim spaces (though RFC says no spaces allowed before size)
			line = bytes.TrimSpace(line)
			
			chunkSize, err := strconv.ParseInt(string(line), 16, 64)
			if err != nil {
				// Malformed chunk size
				return CheckResult{Error: err, Complete: false}
			}

			// Move past CRLF
			offset += idx + 2

			if chunkSize == 0 {
				// Last chunk found. Now look for Trailer termination (Empty line)
				// The trailer part ends with CRLF.
				// Since we just consumed the CRLF after "0", we look for the next "\r\n".
				// If there are no trailers, the next bytes should be "\r\n".
				
				trailerEnd := bytes.Index(bodyData[offset:], []byte("\r\n"))
				if trailerEnd == -1 {
					return CheckResult{Complete: false}
				}
				
				// Found the final CRLF.
				totalConsumed := headerBodySep + offset + trailerEnd + 2
				return CheckResult{Complete: true, BytesConsumed: totalConsumed}
			}

			// Skip Chunk Data + CRLF
			// Check if we have enough data
			if len(bodyData[offset:]) < int(chunkSize)+2 {
				return CheckResult{Complete: false}
			}
			offset += int(chunkSize) + 2
		}
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
