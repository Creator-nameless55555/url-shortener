package shortener

import (
	"crypto/rand"
	"io"
)

func randomCode() (string, error) { return codeFrom(rand.Reader) }

func codeFrom(source io.Reader) (string, error) {
	var code [CodeLength]byte
	var buffer [16]byte
	n := 0
	for n < len(code) {
		if _, err := io.ReadFull(source, buffer[:]); err != nil {
			return "", err
		}
		for _, b := range buffer {
			// В 252 ровно четыре раза помещается 63. Отбрасываем 252..255,
			// иначе первые четыре символа алфавита выпадали бы чаще остальных.
			if b >= 252 {
				continue
			}
			code[n] = Alphabet[int(b)%len(Alphabet)]
			n++
			if n == len(code) {
				break
			}
		}
	}
	return string(code[:]), nil
}
