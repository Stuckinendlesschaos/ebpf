package ebpf

import (
	"encoding/binary"
	"io/ioutil"
	"os"
	"path/filepath"
	"testing"
)

type entity uint32

func (e entity) MarshalBinary() ([]byte, error) {
	buf := make([]byte, 4)
	binary.LittleEndian.PutUint32(buf, uint32(e))
	return buf, nil
}

func (e *entity) UnmarshalBinary(buf []byte) error {
	*e = entity(binary.LittleEndian.Uint32(buf))
	return nil
}

func TestMap(t *testing.T) {
	m, err := NewMap(&MapSpec{
		Type:       Array,
		KeySize:    4,
		ValueSize:  4,
		MaxEntries: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()

	t.Log(m)

	if err := m.Put(entity(0), entity(42)); err != nil {
		t.Fatal("Can't put:", err)
	}
	if err := m.Put(entity(1), entity(4242)); err != nil {
		t.Fatal("Can't put:", err)
	}

	var v entity
	if ok, err := m.Get(entity(0), &v); err != nil {
		t.Fatal("Can't get:", err)
	} else if !ok {
		t.Fatal("Key doesn't exist")
	}
	if v != 42 {
		t.Error("Want value 42, got", v)
	}

	var k entity
	if ok, err := m.GetNextKey(entity(0), &k); err != nil {
		t.Fatal("Can't get:", err)
	} else if !ok {
		t.Fatal("Key doesn't exist")
	}
	if k != 1 {
		t.Error("Want key 1, got", k)
	}
}

func TestMapPin(t *testing.T) {
	m, err := NewMap(&MapSpec{
		Type:       Array,
		KeySize:    4,
		ValueSize:  4,
		MaxEntries: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()

	if err := m.Put(entity(0), entity(42)); err != nil {
		t.Fatal("Can't put:", err)
	}

	tmp, err := ioutil.TempDir("/sys/fs/bpf", "ebpf-test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmp)

	path := filepath.Join(tmp, "map")
	if err := m.Pin(path); err != nil {
		t.Fatal(err)
	}
	m.Close()

	m, err = LoadMap(path)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()

	var v entity
	if ok, err := m.Get(entity(0), &v); err != nil {
		t.Fatal("Can't get:", err)
	} else if !ok {
		t.Fatal("Key doesn't exist")
	}
	if v != 42 {
		t.Error("Want value 42, got", v)
	}
}
