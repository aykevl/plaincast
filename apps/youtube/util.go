package youtube

import (
	"io"
	"io/ioutil"
	"log"
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
	if resp.ContentLength < 0 {
		buf, err := ioutil.ReadAll(resp.Body)
		if err != nil {
			panic(err)
		}
		return buf

	} else {
		buf := make([]byte, resp.ContentLength)
		_, err := io.ReadFull(resp.Body, buf)
		if err != nil {
			panic(err)
		}
		return buf
	}
}

func handle(err error, message string) {
	if err != nil {
		log.Fatalf("%s: %s\n", message, err)
	}
}
