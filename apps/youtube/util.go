package youtube

import (
	"io"
	"math/rand"
	"net/http"
	"net/url"
)

// zx generates a random string of bytes that is 12 characters long.
// It is being used by some (unofficial) Google APIs.
func zx() []byte {
	buf := make([]byte, 12)
	for i, _ := range buf {
		buf[i] = 'a' + byte(rand.Intn(26))
	}

	return buf
}

func mustGet(url string) []byte {
	resp, err := http.Get(url)
	if err != nil {
		panic(err)
	}
	defer resp.Body.Close()

	return processRequest(resp)
}

func mustPostForm(url string, values url.Values) []byte {
	resp, err := http.PostForm(url, values)
	if err != nil {
		panic(err)
	}
	defer resp.Body.Close()

	return processRequest(resp)
}

func processRequest(resp *http.Response) []byte {
	var buf []byte
	if resp.ContentLength < 0 {
		buf = make([]byte, 4096)
		n, err := resp.Body.Read(buf)
		if err != nil && err != io.EOF {
			panic(err)
		}
		if n == 4096 {
			panic("data is bigger than buffer")
		}
		buf = buf[:n]
	} else {
		buf = make([]byte, resp.ContentLength)
		n, err := resp.Body.Read(buf)
		if err != nil && err != io.EOF {
			panic(err)
		}
		if int64(n) != resp.ContentLength {
			panic("data received not of the right length")
		}
		buf = buf[:n]
	}

	return buf
}
