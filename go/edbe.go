// Package edbe is the Go SDK of edbe, an embedded database: documents, relationships, search
// and permissions in one file. It is plain Go (no cgo) and carries the engine for each
// platform it supports, so `go get` is the whole installation.
//
//	db, err := edbe.Open("app.edbe")
//	defer db.Close()
//	db.Put("users:ada", map[string]any{"name": "Ada", "city": "Oslo"})
//	rows, err := db.Query("SELECT name FROM users WHERE city = ?", "Oslo")   // [map[name:Ada]]
//
// A DB may be used from many goroutines. A Tx belongs to the goroutine that began it, until
// Commit or Rollback, and so does every Stmt prepared on it.
package edbe

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"unsafe"
)

// The engine's statuses (DB_Status in edbe.h): Error.Status holds one of the negative ones.
const (
	StatusOK          = 0
	StatusRow         = 1
	StatusDone        = 2
	StatusNotFound    = -1
	StatusExists      = -2
	StatusInvalidArg  = -3
	StatusSyntax      = -4
	StatusSchema      = -5
	StatusNoIndex     = -6
	StatusConflict    = -7
	StatusBusy        = -8
	StatusReadOnly    = -9
	StatusIO          = -10
	StatusCorrupt     = -11
	StatusVersion     = -12
	StatusNoMem       = -13
	StatusLimit       = -14
	StatusTxState     = -15
	StatusInterrupted = -16
	StatusDenied      = -17
	StatusConstraint  = -18
	StatusInternal    = -99
)

// Each status's name, and the message an error carries when the engine gives none.
var statuses = map[int32][2]string{
	StatusOK:          {"DB_OK", ""},
	StatusRow:         {"DB_ROW", ""},
	StatusDone:        {"DB_DONE", ""},
	StatusNotFound:    {"DB_ERR_NOT_FOUND", "not found"},
	StatusExists:      {"DB_ERR_EXISTS", "exists already"},
	StatusInvalidArg:  {"DB_ERR_INVALID_ARG", "invalid argument"},
	StatusSyntax:      {"DB_ERR_SYNTAX", "syntax error"},
	StatusSchema:      {"DB_ERR_SCHEMA", "schema error"},
	StatusNoIndex:     {"DB_ERR_NO_INDEX", "no index for the query"},
	StatusConflict:    {"DB_ERR_CONFLICT", "conflict"},
	StatusBusy:        {"DB_ERR_BUSY", "the database is busy"},
	StatusReadOnly:    {"DB_ERR_READONLY", "read-only"},
	StatusIO:          {"DB_ERR_IO", "input/output error"},
	StatusCorrupt:     {"DB_ERR_CORRUPT", "the file is corrupt"},
	StatusVersion:     {"DB_ERR_VERSION", "incompatible engine version"},
	StatusNoMem:       {"DB_ERR_NOMEM", "out of memory"},
	StatusLimit:       {"DB_ERR_LIMIT", "a limit was exceeded"},
	StatusTxState:     {"DB_ERR_TX_STATE", "not possible in the transaction's state"},
	StatusInterrupted: {"DB_ERR_INTERRUPTED", "interrupted"},
	StatusDenied:      {"DB_ERR_DENIED", "permission denied"},
	StatusConstraint:  {"DB_ERR_CONSTRAINT", "a constraint refused the write"},
	StatusInternal:    {"DB_ERR_INTERNAL", "internal error"},
}

// Error is an engine error: its status (StatusConstraint, ...), the status's name
// (DB_ERR_CONSTRAINT, ...), its message and, for an error in SQL text, the byte offset it
// points at (-1 when none).
type Error struct {
	Status  int
	Name    string
	Message string
	Pos     int
}

func (e *Error) Error() string {
	if e.Message == "" {
		return e.Name
	}
	return e.Name + ": " + e.Message
}

// newError is the error of status st with the engine's message and position; a status the
// engine gave no message for gets the package's.
func newError(st int32, msg string, pos int32) *Error {
	s, ok := statuses[st]
	if !ok {
		s = [2]string{dbStatusStr(st), ""}
	}
	if msg == "" {
		msg = s[1]
	}
	return &Error{Status: int(st), Name: s[0], Message: msg, Pos: int(pos)}
}

func errorOf(st int32, msg string) error { return newError(st, msg, -1) }

func dbClosed() error { return errorOf(StatusInvalidArg, "the database is closed") }

func txEnded() error { return errorOf(StatusTxState, "the transaction has ended") }

// Options for Open. Mode is "default", "durable" or "fast"; the zero value is "default".
type Options struct {
	Mode     string
	ReadOnly bool
}

// DB is an open database.
type DB struct {
	h     uintptr
	mu    sync.Mutex
	stmts map[*Stmt]struct{} // prepared on the DB: finalized by Close
}

// Tx is a transaction, from Begin or BeginAs.
type Tx struct {
	h     uintptr
	db    *DB
	done  bool
	stmts []*Stmt // prepared on the transaction: finalized when it ends
}

// Stmt is a prepared statement, from Prepare. One from DB.Prepare may be used from any
// goroutine (one call at a time) until Close or the DB's Close; one from Tx.Prepare belongs to
// the transaction's goroutine and is closed when the transaction ends.
type Stmt struct {
	mu sync.Mutex
	h  uintptr
	db *DB
	tx *Tx // nil for a statement of the DB
}

// Version is the engine's version, e.g. "0.1.0".
func Version() string {
	if load() != nil {
		return ""
	}
	return dbVersion()
}

// ABIVersion is the engine's C ABI version.
func ABIVersion() (major, minor int) {
	if load() != nil {
		return 0, 0
	}
	v := dbABIVersion()
	return int(v >> 16), int(v & 0xffff)
}

// Open opens or creates a database file; ":memory:" is a database that lives only in this
// process.
func Open(path string, options ...Options) (*DB, error) {
	if err := load(); err != nil {
		return nil, err
	}
	var o dbOptions
	dbOptionsInit(&o)
	if len(options) > 0 {
		switch options[0].Mode {
		case "", "default":
			o.mode = 1
		case "durable":
			o.mode = 2
		case "fast":
			o.mode = 3
		default:
			return nil, errorOf(StatusInvalidArg, fmt.Sprintf("unknown mode %q (default, durable or fast)", options[0].Mode))
		}
		if options[0].ReadOnly {
			o.readOnly = 1
		}
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	var h uintptr
	if st := dbOpen(path, &o, &h); st < 0 {
		return nil, newError(st, dbErrmsg(0), -1)
	}
	return &DB{h: h}, nil
}

// Close closes the database, and every statement prepared on it.
func (db *DB) Close() error {
	if db.h == 0 {
		return nil
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	db.mu.Lock()
	stmts := db.stmts
	db.stmts = nil
	db.mu.Unlock()
	for s := range stmts {
		s.finalize()
	}
	if st := dbClose(db.h); st < 0 {
		return db.err(st)
	}
	db.h = 0
	return nil
}

// Query runs one statement, `?` bound to params in order, and returns its rows.
func (db *DB) Query(sql string, params ...any) ([]map[string]any, error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	return db.run(sql, params)
}

// Execute runs a statement with params, or a script of several statements without.
func (db *DB) Execute(sql string, params ...any) error {
	if db.h == 0 {
		return dbClosed()
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if len(params) == 0 {
		if st := dbExecute(db.h, sql); st < 0 {
			return db.err(st)
		}
		return nil
	}
	_, err := db.run(sql, params)
	return err
}

// Put writes doc at the address key ("users:ada"), created or replaced. doc is anything
// encoding/json encodes, or JSON text as a string, []byte or json.RawMessage; text or bytes
// that are not JSON are stored as a bytes value.
func (db *DB) Put(key string, doc any) error {
	_, err := db.Query("PUT ? ?", key, putDoc(doc))
	return noPos(err)
}

// Get reads the record at key; nil when there is none.
func (db *DB) Get(key string) (map[string]any, error) { return first(db.Query("GET ?", key)) }

// Delete removes the record at key.
func (db *DB) Delete(key string) error { _, err := db.Query("DELETE ?", key); return noPos(err) }

// Insert adds doc to collection with a generated id, and returns the id: a string, or an
// int64 in an ID INTEGER collection.
func (db *DB) Insert(collection string, doc any) (any, error) {
	if !ident.MatchString(collection) {
		return nil, errorOf(StatusInvalidArg, "not a collection name: "+collection)
	}
	row, err := first(db.Query("INSERT INTO "+collection+" (doc) VALUES (?)", asDoc(doc)))
	if err != nil || row == nil {
		return nil, err
	}
	return row["id"], nil
}

// SetLabel sets the label every later transaction on the handle runs under, for every
// goroutine; "" clears it. A trusted, host-level call: statements cannot change a label.
func (db *DB) SetLabel(label string) error { return db.setName(dbSetLabel, label) }

// SetPrincipal sets the record key of the acting end user ("users:42") for every later
// transaction on the handle; row-level policies read it as @principal. "" clears it.
func (db *DB) SetPrincipal(principal string) error { return db.setName(dbSetPrincipal, principal) }

func (db *DB) setName(fn func(uintptr, *byte) int32, name string) error {
	if db.h == 0 {
		return dbClosed()
	}
	p, err := cName(name)
	if err != nil {
		return err
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	st := fn(db.h, p)
	runtime.KeepAlive(p)
	if st < 0 {
		return db.err(st)
	}
	return nil
}

// Begin starts a transaction, a write transaction when write is true. The goroutine stays on
// its thread until Commit or Rollback.
func (db *DB) Begin(write bool) (*Tx, error) { return db.begin(write, false, "", "") }

// BeginAs is Begin with the transaction's label and principal set: what a server does for
// each request. An empty label or principal keeps the handle's.
func (db *DB) BeginAs(write bool, label, principal string) (*Tx, error) {
	return db.begin(write, true, label, principal)
}

func (db *DB) begin(write, as bool, label, principal string) (*Tx, error) {
	if db.h == 0 {
		return nil, dbClosed()
	}
	var l, p *byte
	if as {
		var err error
		if l, err = cName(label); err != nil {
			return nil, err
		}
		if p, err = cName(principal); err != nil {
			return nil, err
		}
	}
	runtime.LockOSThread()
	mode := int32(0)
	if write {
		mode = 1
	}
	var h uintptr
	var st int32
	if as {
		st = dbBeginAs(db.h, mode, l, p, &h)
	} else {
		st = dbBegin(db.h, mode, &h)
	}
	runtime.KeepAlive(l)
	runtime.KeepAlive(p)
	if st < 0 {
		err := db.err(st)
		runtime.UnlockOSThread()
		return nil, err
	}
	return &Tx{h: h, db: db}, nil
}

// Prepare parses a statement once for many runs (Stmt.Query, Exec, RunMany).
func (db *DB) Prepare(sql string) (*Stmt, error) {
	if db.h == 0 {
		return nil, dbClosed()
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	var h uintptr
	if st := dbPrepare(db.h, sql, &h); st < 0 {
		return nil, db.err(st)
	}
	s := &Stmt{h: h, db: db}
	db.mu.Lock()
	if db.stmts == nil {
		db.stmts = map[*Stmt]struct{}{}
	}
	db.stmts[s] = struct{}{}
	db.mu.Unlock()
	return s, nil
}

// Query runs one statement in the transaction.
func (tx *Tx) Query(sql string, params ...any) ([]map[string]any, error) {
	if tx.done {
		return nil, txEnded()
	}
	return tx.run(sql, params)
}

// Execute runs a statement with params, or a script without, in the transaction.
func (tx *Tx) Execute(sql string, params ...any) error {
	if tx.done {
		return txEnded()
	}
	if len(params) == 0 {
		if st := dbTxExecute(tx.h, sql); st < 0 {
			return tx.err(st)
		}
		return nil
	}
	_, err := tx.run(sql, params)
	return err
}

// Put, Get, Delete and Insert as on DB, in the transaction.
func (tx *Tx) Put(key string, doc any) error {
	_, err := tx.Query("PUT ? ?", key, putDoc(doc))
	return noPos(err)
}

func (tx *Tx) Get(key string) (map[string]any, error) { return first(tx.Query("GET ?", key)) }

func (tx *Tx) Delete(key string) error { _, err := tx.Query("DELETE ?", key); return noPos(err) }

func (tx *Tx) Insert(collection string, doc any) (any, error) {
	if !ident.MatchString(collection) {
		return nil, errorOf(StatusInvalidArg, "not a collection name: "+collection)
	}
	row, err := first(tx.Query("INSERT INTO "+collection+" (doc) VALUES (?)", asDoc(doc)))
	if err != nil || row == nil {
		return nil, err
	}
	return row["id"], nil
}

// SetLabel sets the label of this transaction only, over the handle's; "" clears it for the
// transaction.
func (tx *Tx) SetLabel(label string) error { return tx.setName(dbTxSetLabel, label) }

// SetPrincipal sets the principal of this transaction only; "" clears it for the transaction.
func (tx *Tx) SetPrincipal(principal string) error { return tx.setName(dbTxSetPrinc, principal) }

func (tx *Tx) setName(fn func(uintptr, *byte) int32, name string) error {
	if tx.done {
		return txEnded()
	}
	p, err := cName(name)
	if err != nil {
		return err
	}
	st := fn(tx.h, p)
	runtime.KeepAlive(p)
	if st < 0 {
		return tx.err(st)
	}
	return nil
}

// Prepare parses a statement for many runs in the transaction. Use it on the transaction's
// goroutine; it is closed when the transaction ends.
func (tx *Tx) Prepare(sql string) (*Stmt, error) {
	if tx.done {
		return nil, txEnded()
	}
	var h uintptr
	if st := dbTxPrepare(tx.h, sql, &h); st < 0 {
		return nil, tx.err(st)
	}
	s := &Stmt{h: h, db: tx.db, tx: tx}
	tx.stmts = append(tx.stmts, s)
	return s, nil
}

// Commit makes the transaction's writes durable and ends it.
func (tx *Tx) Commit() error { return tx.end(dbCommit) }

// Rollback discards the transaction's writes and ends it.
func (tx *Tx) Rollback() error { return tx.end(dbRollback) }

func (tx *Tx) end(fn func(uintptr) int32) error {
	if tx.done {
		return txEnded()
	}
	st := fn(tx.h)
	var err error
	if st < 0 {
		err = tx.db.err(st) // the transaction is gone: its message is on the handle
	}
	for _, s := range tx.stmts {
		s.finalize()
	}
	tx.stmts = nil
	tx.done = true
	tx.h = 0
	runtime.UnlockOSThread()
	return err
}

// Query runs the statement once with params and returns its rows.
func (s *Stmt) Query(params ...any) ([]map[string]any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.usable(); err != nil {
		return nil, err
	}
	if s.tx == nil {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
	}
	return runStmt(s.h, params, s.last)
}

// Exec runs the statement once with params and discards its rows.
func (s *Stmt) Exec(params ...any) error {
	_, err := s.Query(params...)
	return err
}

// RunMany runs the statement once for each list of parameters, in one call into the engine,
// and returns the rows of every run in order. Each list holds one value per placeholder; the
// lists are sent as JSON, so a value is what encoding/json makes of it (an object or array is
// bound as a document). A run that fails ends the call: on a DB the runs before it are
// committed, each by itself; in a transaction they are undone with it.
func (s *Stmt) RunMany(rows [][]any) ([]map[string]any, error) {
	if rows == nil {
		rows = [][]any{}
	}
	text, err := json.Marshal(rows)
	if err != nil {
		return nil, errorOf(StatusInvalidArg, "parameter lists: "+err.Error())
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.usable(); err != nil {
		return nil, err
	}
	if s.tx == nil {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
	}
	var out dbSlice
	st := dbStmtRunMany(s.h, dbSlice{data: unsafe.Pointer(&text[0]), length: uintptr(len(text))}, &out)
	runtime.KeepAlive(text)
	if st < 0 {
		msg, pos := s.last()
		return nil, newError(st, msg, pos)
	}
	return decodeRows(out)
}

// Close releases the statement; a second Close returns nil.
func (s *Stmt) Close() error {
	if s.tx == nil {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		s.db.mu.Lock()
		delete(s.db.stmts, s)
		s.db.mu.Unlock()
	}
	s.finalize()
	return nil
}

func (s *Stmt) finalize() {
	s.mu.Lock()
	if s.h != 0 {
		dbStmtFinalize(s.h)
		s.h = 0
	}
	s.mu.Unlock()
}

func (s *Stmt) usable() error {
	if s.tx != nil && s.tx.done {
		return txEnded()
	}
	if s.h == 0 {
		if s.tx == nil && s.db.h == 0 {
			return dbClosed()
		}
		return errorOf(StatusInvalidArg, "the statement is closed")
	}
	return nil
}

// last is the message and position of the statement's failed call.
func (s *Stmt) last() (string, int32) {
	if s.tx != nil {
		return s.tx.last()
	}
	return s.db.last()
}

var ident = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func (db *DB) last() (string, int32) { return dbErrmsg(db.h), dbErrpos(db.h) }

func (tx *Tx) last() (string, int32) { return dbTxErrmsg(tx.h), dbTxErrpos(tx.h) }

func (db *DB) err(st int32) error { msg, pos := db.last(); return newError(st, msg, pos) }

func (tx *Tx) err(st int32) error { msg, pos := tx.last(); return newError(st, msg, pos) }

func (db *DB) run(sql string, params []any) ([]map[string]any, error) {
	if db.h == 0 {
		return nil, dbClosed()
	}
	var stmt uintptr
	if st := dbPrepare(db.h, sql, &stmt); st < 0 {
		return nil, db.err(st)
	}
	defer dbStmtFinalize(stmt)
	return runStmt(stmt, params, db.last)
}

func (tx *Tx) run(sql string, params []any) ([]map[string]any, error) {
	var stmt uintptr
	if st := dbTxPrepare(tx.h, sql, &stmt); st < 0 {
		return nil, tx.err(st)
	}
	defer dbStmtFinalize(stmt)
	return runStmt(stmt, params, tx.last)
}

// runStmt binds params, runs the statement once and decodes its rows (one JSON array); last
// gives the message and position of a failure.
func runStmt(stmt uintptr, params []any, last func() (string, int32)) ([]map[string]any, error) {
	var pin runtime.Pinner
	defer pin.Unpin()
	values := make([]dbValue, len(params))
	for i, p := range params {
		v, err := toValue(p)
		if err != nil {
			return nil, errorOf(StatusInvalidArg, fmt.Sprintf("parameter %d: %v", i+1, err))
		}
		if len(v) > 0 {
			pin.Pin(&v[0])
			values[i].data = unsafe.Pointer(&v[0])
			values[i].length = uintptr(len(v) - 1)
			values[i].typ = uint32(v[len(v)-1])
		} else {
			values[i].typ = typeNull
		}
	}
	var vp *dbValue
	if len(values) > 0 {
		pin.Pin(&values[0])
		vp = &values[0]
	}
	var out dbSlice
	if st := dbStmtRun(stmt, vp, uint32(len(values)), &out); st < 0 {
		msg, pos := last()
		return nil, newError(st, msg, pos)
	}
	return decodeRows(out)
}

// decodeRows decodes the engine's rows, the text of one JSON array (valid until the
// statement's next call). A whole number is an int64, or a uint64 past int64; any other
// number a float64.
func decodeRows(out dbSlice) ([]map[string]any, error) {
	rows := []map[string]any{}
	if out.length == 0 {
		return rows, nil
	}
	dec := json.NewDecoder(bytes.NewReader(unsafe.Slice((*byte)(out.data), out.length)))
	dec.UseNumber()
	if err := dec.Decode(&rows); err != nil {
		return nil, errorOf(StatusInternal, "rows are not JSON: "+err.Error())
	}
	for _, r := range rows {
		for k, v := range r {
			r[k] = numbers(v)
		}
	}
	return rows, nil
}

func numbers(v any) any {
	switch x := v.(type) {
	case json.Number:
		s := string(x)
		if !strings.ContainsAny(s, ".eE") {
			if i, err := strconv.ParseInt(s, 10, 64); err == nil {
				return i
			}
			if u, err := strconv.ParseUint(s, 10, 64); err == nil {
				return u
			}
		}
		f, _ := strconv.ParseFloat(s, 64)
		return f
	case map[string]any:
		for k, e := range x {
			x[k] = numbers(e)
		}
	case []any:
		for i, e := range x {
			x[i] = numbers(e)
		}
	}
	return v
}

// cName is a label or principal as a C string; nil (NULL) for "".
func cName(s string) (*byte, error) {
	if s == "" {
		return nil, nil
	}
	if strings.IndexByte(s, 0) >= 0 {
		return nil, errorOf(StatusInvalidArg, "a label or principal cannot hold a NUL byte")
	}
	b := make([]byte, len(s)+1)
	copy(b, s)
	return &b[0], nil
}

type rawDoc []byte

// asDoc turns a document into JSON text, bound as a JSON value.
func asDoc(doc any) any {
	switch d := doc.(type) {
	case json.RawMessage:
		return rawDoc(d)
	case string:
		return rawDoc(d)
	case []byte:
		return rawDoc(d)
	}
	b, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	return rawDoc(b)
}

// putDoc is asDoc for Put, where text or bytes that are not JSON are a bytes value, as the
// engine's db_put stores them.
func putDoc(doc any) any {
	var b []byte
	switch d := doc.(type) {
	case json.RawMessage:
		b = d
	case string:
		b = []byte(d)
	case []byte:
		b = d
	default:
		return asDoc(doc)
	}
	if json.Valid(b) {
		return rawDoc(b)
	}
	return b
}

// toValue encodes one parameter: its bytes followed by one byte, its DB_Type (empty for NULL).
func toValue(p any) ([]byte, error) {
	le := func(u uint64, t byte) []byte {
		b := make([]byte, 9)
		for i := 0; i < 8; i++ {
			b[i] = byte(u >> (8 * i))
		}
		b[8] = t
		return b
	}
	withType := func(b []byte, t byte) []byte {
		out := make([]byte, len(b)+1)
		copy(out, b)
		out[len(b)] = t
		return out
	}
	switch x := p.(type) {
	case nil:
		return nil, nil
	case error:
		return nil, x
	case bool:
		if x {
			return []byte{1, typeBool}, nil
		}
		return []byte{0, typeBool}, nil
	case int:
		return le(uint64(int64(x)), typeInt64), nil
	case int8:
		return le(uint64(int64(x)), typeInt64), nil
	case int16:
		return le(uint64(int64(x)), typeInt64), nil
	case int32:
		return le(uint64(int64(x)), typeInt64), nil
	case int64:
		return le(uint64(x), typeInt64), nil
	case uint:
		return le(uint64(x), typeUint64), nil
	case uint8:
		return le(uint64(x), typeInt64), nil
	case uint16:
		return le(uint64(x), typeInt64), nil
	case uint32:
		return le(uint64(x), typeInt64), nil
	case uint64:
		return le(x, typeUint64), nil
	case float32:
		return le(math.Float64bits(float64(x)), typeFloat64), nil
	case float64:
		return le(math.Float64bits(x), typeFloat64), nil
	case string:
		return withType([]byte(x), typeString), nil
	case []byte:
		return withType(x, typeBytes), nil
	case rawDoc:
		return withType(x, typeJSON), nil
	case json.RawMessage:
		return withType(x, typeJSON), nil
	}
	b, err := json.Marshal(p)
	if err != nil {
		return nil, err
	}
	return withType(b, typeJSON), nil
}

// first is the first row of a statement the package wrote itself (Get, Insert).
func first(rows []map[string]any, err error) (map[string]any, error) {
	if err != nil || len(rows) == 0 {
		return nil, noPos(err)
	}
	return rows[0], nil
}

// noPos clears the position of an error in a statement the package wrote itself: it points
// into no text of the caller's.
func noPos(err error) error {
	if e, ok := err.(*Error); ok {
		e.Pos = -1
	}
	return err
}
