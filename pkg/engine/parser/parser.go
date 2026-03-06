package parser

import (
	"bytes"
	"strconv"
)

const maxInt = int(^uint(0) >> 1)
const maxInt64 = int64(^uint64(0) >> 1)

var (
	headerEnd  = []byte("\r\n\r\n")
	headerCL   = []byte("Content-Length:")
	headerTE   = []byte("Transfer-Encoding:")
	valChunked = []byte("chunked")
)

type CheckResult struct {
	Complete      bool // 요청이 완전히 수신되었는가?
	BytesConsumed int  // 요청 전체의 길이 (Header + Body)
	Error         error
}

const MaxHeaderSize = 8 * 1024 // 8KB

func CheckRequest(data []byte) CheckResult {
	// 1. 헤더 경계 검색
	headerEndIdx := bytes.Index(data, headerEnd)
	if headerEndIdx == -1 {
		// DoS Protection: If data exceeds limit and header end not found, reject.
		if len(data) > MaxHeaderSize {
			return CheckResult{
				Complete: false,
				Error:    strconv.ErrRange, // Or a custom error
			}
		}
		return CheckResult{Complete: false}
	}

	// DoS Protection: If header is found but too large
	if headerEndIdx > MaxHeaderSize {
		return CheckResult{
			Complete: false,
			Error:    strconv.ErrRange,
		}
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
			cl, err := parseInt(val)
			if err != nil {
				return CheckResult{Complete: false, Error: err}
			}
			if cl < 0 {
				return CheckResult{Complete: false, Error: strconv.ErrSyntax}
			}
			contentLength = cl
			continue
		}

		// Check for Transfer-Encoding
		if len(line) > len(headerTE) && bytes.EqualFold(line[:len(headerTE)], headerTE) {
			val := line[len(headerTE):]
			val = bytes.TrimSpace(val)
			// Optimized: Check if contains "chunked" (case-insensitive) without allocation
			// bytes.ToLower allocates.
			// Simple implementation: "chunked" is 7 chars.
			if len(val) >= len(valChunked) {
				// Fast path: exact match
				if bytes.EqualFold(val, valChunked) {
					isChunked = true
				} else {
					// Slow path: contains check (manual)
					// Helper to avoid allocation
					if containsCaseInsensitive(val, valChunked) {
						isChunked = true
					}
				}
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

			chunkSize, err := parseHexInt(line)
			if err != nil {
				// Malformed chunk size
				return CheckResult{Error: err, Complete: false}
			}

			// Move past CRLF
			offset += idx + 2

			if chunkSize == 0 {
				// Last chunk found. If there are no trailers, the next bytes are just CRLF.
				if len(bodyData[offset:]) >= 2 && bytes.HasPrefix(bodyData[offset:], []byte("\r\n")) {
					totalConsumed := headerBodySep + offset + 2
					return CheckResult{Complete: true, BytesConsumed: totalConsumed}
				}

				// Otherwise, trailer section ends with CRLFCRLF.
				trailerEnd := bytes.Index(bodyData[offset:], headerEnd)
				if trailerEnd == -1 {
					return CheckResult{Complete: false}
				}

				totalConsumed := headerBodySep + offset + trailerEnd + len(headerEnd)
				return CheckResult{Complete: true, BytesConsumed: totalConsumed}
			}

			// Skip Chunk Data + CRLF
			if chunkSize > maxInt64-2 || chunkSize > int64(maxInt-2) {
				return CheckResult{Complete: false, Error: strconv.ErrRange}
			}
			if int64(len(bodyData[offset:])) < chunkSize+2 {
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

// parseInt parses a decimal integer from a byte slice (Zero-Alloc).
func parseInt(b []byte) (int, error) {
	if len(b) == 0 {
		return 0, strconv.ErrSyntax
	}

	neg := false
	if b[0] == '-' {
		neg = true
		b = b[1:]
	} else if b[0] == '+' {
		b = b[1:]
	}

	if len(b) == 0 {
		return 0, strconv.ErrSyntax
	}

	n := 0
	for _, ch := range b {
		if ch < '0' || ch > '9' {
			return 0, strconv.ErrSyntax
		}
		digit := int(ch - '0')
		if n > (maxInt-digit)/10 {
			return 0, strconv.ErrRange
		}
		n = n*10 + digit
	}

	if neg {
		n = -n
	}
	return n, nil
}

// parseHexInt parses a hexadecimal integer from a byte slice (Zero-Alloc).
func parseHexInt(b []byte) (int64, error) {
	if len(b) == 0 {
		return 0, strconv.ErrSyntax
	}

	var n int64
	for _, ch := range b {
		var val int64
		switch {
		case ch >= '0' && ch <= '9':
			val = int64(ch - '0')
		case ch >= 'a' && ch <= 'f':
			val = int64(ch - 'a' + 10)
		case ch >= 'A' && ch <= 'F':
			val = int64(ch - 'A' + 10)
		default:
			return 0, strconv.ErrSyntax
		}
		if n > (maxInt64-val)/16 {
			return 0, strconv.ErrRange
		}
		n = n*16 + val
	}
	return n, nil
}

// containsCaseInsensitive checks if s contains substr (case-insensitive).
// substr must be lower-case.
func containsCaseInsensitive(s, substr []byte) bool {
	if len(substr) == 0 {
		return true
	}
	if len(s) < len(substr) {
		return false
	}
	for i := 0; i <= len(s)-len(substr); i++ {
		match := true
		for j := 0; j < len(substr); j++ {
			c := s[i+j]
			if c >= 'A' && c <= 'Z' {
				c += 'a' - 'A'
			}
			if c != substr[j] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}
