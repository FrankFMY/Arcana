package arcana

import (
	"testing"

	"github.com/jackc/pglogrepl"
	"github.com/stretchr/testify/assert"
)

func TestWALListener_extractID(t *testing.T) {
	l := &WALListener{relations: make(map[uint32]relationInfo)}

	rel := relationInfo{
		Name:    "users",
		Columns: []string{"id", "name", "email"},
	}

	tuple := &pglogrepl.TupleData{
		Columns: []*pglogrepl.TupleDataColumn{
			{DataType: pglogrepl.TupleDataTypeText, Data: []byte("abc-123")},
			{DataType: pglogrepl.TupleDataTypeText, Data: []byte("Alice")},
			{DataType: pglogrepl.TupleDataTypeText, Data: []byte("alice@example.com")},
		},
	}

	id := l.extractID(tuple, rel)
	assert.Equal(t, "abc-123", id)
}

func TestWALListener_extractID_NilTuple(t *testing.T) {
	l := &WALListener{relations: make(map[uint32]relationInfo)}
	rel := relationInfo{Name: "users", Columns: []string{"id"}}

	id := l.extractID(nil, rel)
	assert.Equal(t, "", id)
}

func TestWALListener_changedColumns(t *testing.T) {
	l := &WALListener{relations: make(map[uint32]relationInfo)}

	rel := relationInfo{
		Name:    "users",
		Columns: []string{"id", "name", "email"},
	}

	oldTuple := &pglogrepl.TupleData{
		Columns: []*pglogrepl.TupleDataColumn{
			{DataType: pglogrepl.TupleDataTypeText, Data: []byte("abc-123")},
			{DataType: pglogrepl.TupleDataTypeText, Data: []byte("Alice")},
			{DataType: pglogrepl.TupleDataTypeText, Data: []byte("alice@old.com")},
		},
	}

	newTuple := &pglogrepl.TupleData{
		Columns: []*pglogrepl.TupleDataColumn{
			{DataType: pglogrepl.TupleDataTypeText, Data: []byte("abc-123")},
			{DataType: pglogrepl.TupleDataTypeText, Data: []byte("Alice")},
			{DataType: pglogrepl.TupleDataTypeText, Data: []byte("alice@new.com")},
		},
	}

	changed := l.changedColumns(oldTuple, newTuple, rel)
	assert.Equal(t, []string{"email"}, changed)
}

func TestWALListener_changedColumns_NilOld(t *testing.T) {
	l := &WALListener{relations: make(map[uint32]relationInfo)}

	rel := relationInfo{
		Name:    "users",
		Columns: []string{"id", "name"},
	}

	newTuple := &pglogrepl.TupleData{
		Columns: []*pglogrepl.TupleDataColumn{
			{DataType: pglogrepl.TupleDataTypeText, Data: []byte("abc")},
			{DataType: pglogrepl.TupleDataTypeText, Data: []byte("Bob")},
		},
	}

	changed := l.changedColumns(nil, newTuple, rel)
	assert.Equal(t, []string{"id", "name"}, changed)
}

func TestNewWALListener_Defaults(t *testing.T) {
	l := NewWALListener(WALConfig{
		ConnString: "postgres://localhost/test",
	})

	assert.Equal(t, "arcana_slot", l.slotName)
	assert.Equal(t, "arcana_pub", l.publication)
	assert.NotNil(t, l.logger)
	assert.Equal(t, 10*1e9, float64(l.standbyTimeout))
}

func TestNewWALListener_CustomConfig(t *testing.T) {
	l := NewWALListener(WALConfig{
		ConnString:  "postgres://localhost/test",
		SlotName:    "my_slot",
		Publication: "my_pub",
	})

	assert.Equal(t, "my_slot", l.slotName)
	assert.Equal(t, "my_pub", l.publication)
}
