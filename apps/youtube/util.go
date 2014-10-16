package youtube

import (
	"errors"
	"io"
	"io/ioutil"
	"log"
	"math/rand"
	"net/http"
	"net/url"
	"strconv"
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

func httpGetBody(url string) ([]byte, error) {
	resp, err := http.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	return processRequest(resp)
}

func httpPostFormBody(url string, values url.Values) ([]byte, error) {
	resp, err := http.PostForm(url, values)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	return processRequest(resp)
}

func processRequest(resp *http.Response) ([]byte, error) {

	if resp.StatusCode != 200 {
		return nil, errors.New("unexpected HTTP status code: " + strconv.Itoa(resp.StatusCode))
	}

	if resp.ContentLength < 0 {
		return ioutil.ReadAll(resp.Body)
	} else {
		buf := make([]byte, resp.ContentLength)
		_, err := io.ReadFull(resp.Body, buf)
		if err != nil {
			return nil, err
		}
		return buf, nil
	}
}

func handle(err error, message string) {
	if err != nil {
		log.Fatalf("%s: %s\n", message, err)
	}
}
