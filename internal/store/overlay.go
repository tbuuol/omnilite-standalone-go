package store

import "sort"

// Reader is read access to the state.
type Reader interface {
	Get(ns, key string) ([]byte, bool, error)
	Scan(ns, prefix string) ([]KV, error)
}

// Overlay keeps writes in memory on top of a read-only state, so a
// transaction can be tried out without touching the database.
type Overlay struct {
	base Reader
	// changes maps ns and key to the new value; nil marks a deletion.
	changes map[string]map[string][]byte
}

func NewOverlay(base Reader) *Overlay {
	return &Overlay{base: base, changes: map[string]map[string][]byte{}}
}

func (o *Overlay) Get(ns, key string) ([]byte, bool, error) {
	if v, ok := o.changes[ns][key]; ok {
		return v, v != nil, nil
	}
	return o.base.Get(ns, key)
}

func (o *Overlay) set(ns, key string, value []byte) {
	m := o.changes[ns]
	if m == nil {
		m = map[string][]byte{}
		o.changes[ns] = m
	}
	m[key] = value
}

func (o *Overlay) Put(ns, key string, value []byte) error {
	if value == nil {
		value = []byte{}
	}
	o.set(ns, key, append([]byte(nil), value...))
	return nil
}

func (o *Overlay) Delete(ns, key string) error {
	o.set(ns, key, nil)
	return nil
}

func (o *Overlay) Scan(ns, prefix string) ([]KV, error) {
	base, err := o.base.Scan(ns, prefix)
	if err != nil {
		return nil, err
	}
	m := o.changes[ns]
	if len(m) == 0 {
		return base, nil
	}
	var res []KV
	for _, kv := range base {
		if _, changed := m[kv.Key]; !changed {
			res = append(res, kv)
		}
	}
	for k, v := range m {
		if v != nil && len(k) >= len(prefix) && k[:len(prefix)] == prefix {
			res = append(res, KV{Key: k, Value: v})
		}
	}
	sort.Slice(res, func(i, j int) bool { return res[i].Key < res[j].Key })
	return res, nil
}
