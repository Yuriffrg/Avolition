// Package ordered provides a JSON object that keeps insertion order, like a
// Python dict. Use it where keys are dynamic and their order is visible:
// catalog.json, annotations, saved names. Go maps marshal with sorted keys.
package ordered

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// Map is an insertion-ordered string-keyed map. The zero value is ready to use.
type Map[V any] struct {
	keys   []string
	values map[string]V
}

func New[V any]() *Map[V] { return &Map[V]{} }

func (m *Map[V]) Len() int { return len(m.keys) }

func (m *Map[V]) Get(key string) (V, bool) {
	value, ok := m.values[key]
	return value, ok
}

// Set adds or replaces a value; replacing keeps the original position, as in Python.
func (m *Map[V]) Set(key string, value V) {
	if m.values == nil {
		m.values = map[string]V{}
	}
	if _, exists := m.values[key]; !exists {
		m.keys = append(m.keys, key)
	}
	m.values[key] = value
}

func (m *Map[V]) Delete(key string) {
	if _, exists := m.values[key]; !exists {
		return
	}
	delete(m.values, key)
	for i, k := range m.keys {
		if k == key {
			m.keys = append(m.keys[:i:i], m.keys[i+1:]...)
			break
		}
	}
}

// Keys returns the keys in insertion order (a copy).
func (m *Map[V]) Keys() []string { return append([]string(nil), m.keys...) }

// Range calls fn in insertion order until it returns false.
func (m *Map[V]) Range(fn func(key string, value V) bool) {
	for _, k := range m.keys {
		if !fn(k, m.values[k]) {
			return
		}
	}
}

func (m *Map[V]) MarshalJSON() ([]byte, error) {
	var buffer bytes.Buffer
	buffer.WriteByte('{')
	for i, k := range m.keys {
		if i > 0 {
			buffer.WriteByte(',')
		}
		key, err := marshal(k)
		if err != nil {
			return nil, err
		}
		value, err := marshal(m.values[k])
		if err != nil {
			return nil, err
		}
		buffer.Write(key)
		buffer.WriteByte(':')
		buffer.Write(value)
	}
	buffer.WriteByte('}')
	return buffer.Bytes(), nil
}

func marshal(value any) ([]byte, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buffer.Bytes(), "\n"), nil
}

func (m *Map[V]) UnmarshalJSON(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	if delim, ok := token.(json.Delim); !ok || delim != '{' {
		return fmt.Errorf("ordered.Map: expected an object")
	}
	*m = Map[V]{}
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		key := token.(string)
		var value V
		if err := decoder.Decode(&value); err != nil {
			return err
		}
		m.Set(key, value)
	}
	_, err = decoder.Token()
	return err
}
