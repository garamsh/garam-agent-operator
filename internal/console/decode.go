package console

import (
	"bytes"
	"encoding/json"
	"errors"
)

// decodeStrict reads exactly one JSON value into out, refusing a field it does not know.
func decodeStrict(body []byte, out any) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		return err
	}
	if decoder.More() {
		return errors.New("more than one value")
	}
	return nil
}
