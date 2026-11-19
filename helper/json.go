package helper

import (
	"bytes"

	"github.com/goccy/go-json"
)

func JSONUnmarshal(data []byte, v interface{}) error {
	dec := json.NewDecoder(bytes.NewBuffer(data))
	dec.UseNumber()
	return dec.Decode(v)
}

func JSONMarshal(v interface{}) ([]byte, error) {
	return json.Marshal(v)
}

func JSONValid(data []byte) bool {
	return json.Valid(data)
}
