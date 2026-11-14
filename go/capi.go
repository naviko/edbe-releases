package edbe

// The engine's C API (include/edbe.h), called through purego: plain Go, no cgo and no C
// compiler. The calls used take pointers (the statement API binds every value), except
// db_stmt_run_many, whose DB_Slice of parameter lists purego passes by value on the
// platforms the package carries an engine for (linux and darwin, amd64 and arm64).

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
)

// value types (DB_Type)
const (
	typeNull    = 0
	typeBool    = 1
	typeInt64   = 2
	typeUint64  = 3
	typeFloat64 = 4
	typeString  = 5
	typeBytes   = 6
	typeJSON    = 7
)

// DB_Slice
type dbSlice struct {
	data   unsafe.Pointer // engine memory
	length uintptr
}

// DB_Value
type dbValue struct {
	data   unsafe.Pointer
	length uintptr
	typ    uint32
	_      uint32
}

// DB_Options, field for field (Go pads it as C does)
type dbOptions struct {
	structSize         uint32
	pageSize           uint32
	durability         int32
	readOnly           uint8
	createIfMissing    uint8
	exclusive          uint8
	_                  uint8
	busyTimeoutMs      uint32
	maxDocumentBytes   uint64
	maxQueryMemory     uint64
	maxTxMemory        uint64
	maxTraversalDepth  uint32
	maxSnapshots       uint32
	reserved0          uint64
	alloc              uintptr
	free               uintptr
	allocCtx           uintptr
	log                uintptr
	logCtx             uintptr
	journal            uint8
	replica            uint8
	_                  [6]uint8
	reserved1          uint64
	wal                uint8
	_                  [7]uint8
	walCheckpointBytes uint64
	mode               uint8
	_                  [7]uint8
}

var (
	dbOptionsInit  func(o *dbOptions)
	dbOpen         func(path string, o *dbOptions, out *uintptr) int32
	dbClose        func(db uintptr) int32
	dbErrmsg       func(db uintptr) string
	dbTxErrmsg     func(tx uintptr) string
	dbErrpos       func(db uintptr) int32
	dbTxErrpos     func(tx uintptr) int32
	dbStatusStr    func(s int32) string
	dbVersion      func() string
	dbABIVersion   func() uint32
	dbSetLabel     func(db uintptr, label *byte) int32
	dbSetPrincipal func(db uintptr, principal *byte) int32
	dbTxSetLabel   func(tx uintptr, label *byte) int32
	dbTxSetPrinc   func(tx uintptr, principal *byte) int32
	dbExecute      func(db uintptr, sql string) int32
	dbPrepare      func(db uintptr, sql string, out *uintptr) int32
	dbTxPrepare    func(tx uintptr, sql string, out *uintptr) int32
	dbStmtRun      func(stmt uintptr, values *dbValue, count uint32, out *dbSlice) int32
	dbStmtRunMany  func(stmt uintptr, params dbSlice, out *dbSlice) int32
	dbStmtFinalize func(stmt uintptr) int32
	dbBegin        func(db uintptr, mode int32, out *uintptr) int32
	dbBeginAs      func(db uintptr, mode int32, label, principal *byte, out *uintptr) int32
	dbTxExecute    func(tx uintptr, sql string) int32
	dbCommit       func(tx uintptr) int32
	dbRollback     func(tx uintptr) int32
)

var (
	loadOnce sync.Once
	loadErr  error
)

// load finds the engine library and binds the API, once. EDBE_LIBRARY names a library file to
// use instead of the one in this package.
func load() error {
	loadOnce.Do(func() {
		path := os.Getenv("EDBE_LIBRARY")
		if path == "" {
			path, loadErr = extractLibrary()
			if loadErr != nil {
				return
			}
		}
		lib, err := purego.Dlopen(path, purego.RTLD_NOW|purego.RTLD_GLOBAL)
		if err != nil {
			loadErr = fmt.Errorf("edbe: cannot load the engine library %s: %w", path, err)
			return
		}
		bind := func(fn any, name string) { purego.RegisterLibFunc(fn, lib, name) }
		bind(&dbOptionsInit, "db_options_init")
		bind(&dbOpen, "db_open")
		bind(&dbClose, "db_close")
		bind(&dbErrmsg, "db_errmsg")
		bind(&dbTxErrmsg, "db_tx_errmsg")
		bind(&dbErrpos, "db_errpos")
		bind(&dbTxErrpos, "db_tx_errpos")
		bind(&dbStatusStr, "db_status_str")
		bind(&dbVersion, "db_version")
		bind(&dbABIVersion, "db_abi_version")
		bind(&dbSetLabel, "db_set_label")
		bind(&dbSetPrincipal, "db_set_principal")
		bind(&dbTxSetLabel, "db_tx_set_label")
		bind(&dbTxSetPrinc, "db_tx_set_principal")
		bind(&dbExecute, "db_execute")
		bind(&dbPrepare, "db_prepare")
		bind(&dbTxPrepare, "db_tx_prepare")
		bind(&dbStmtRun, "db_stmt_run")
		bind(&dbStmtRunMany, "db_stmt_run_many")
		bind(&dbStmtFinalize, "db_stmt_finalize")
		bind(&dbBegin, "db_begin")
		bind(&dbBeginAs, "db_begin_as")
		bind(&dbTxExecute, "db_tx_execute")
		bind(&dbCommit, "db_commit")
		bind(&dbRollback, "db_rollback")
		var o dbOptions
		dbOptionsInit(&o)
		if uintptr(o.structSize) != unsafe.Sizeof(o) {
			loadErr = fmt.Errorf("edbe: the engine library's options are %d bytes, this package's %d: use the library of this package's release", o.structSize, unsafe.Sizeof(o))
		}
	})
	return loadErr
}

// extractLibrary writes the library embedded for this platform into the user's cache
// directory, once per library, and returns its path.
func extractLibrary() (string, error) {
	name := "libedbe.so"
	if runtime.GOOS == "darwin" {
		name = "libedbe.dylib"
	}
	data, err := fs.ReadFile(embedded, "lib/"+runtime.GOOS+"_"+runtime.GOARCH+"/"+name)
	if err != nil {
		return "", fmt.Errorf("edbe: this package holds no engine for %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	sum := sha256.Sum256(data)
	dir, err := os.UserCacheDir()
	if err != nil {
		dir = os.TempDir()
	}
	dir = filepath.Join(dir, "edbe", hex.EncodeToString(sum[:8]))
	path := filepath.Join(dir, name)
	if st, err := os.Stat(path); err == nil && st.Size() == int64(len(data)) {
		return path, nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("edbe: cannot write the engine library: %w", err)
	}
	tmp, err := os.CreateTemp(dir, name+".*")
	if err != nil {
		return "", fmt.Errorf("edbe: cannot write the engine library: %w", err)
	}
	_, werr := tmp.Write(data)
	cerr := tmp.Close()
	if werr != nil || cerr != nil {
		os.Remove(tmp.Name())
		return "", fmt.Errorf("edbe: cannot write the engine library to %s", dir)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		os.Remove(tmp.Name())
		return "", fmt.Errorf("edbe: cannot write the engine library: %w", err)
	}
	return path, nil
}
