//! edbe for Rust: an embedded database (documents, relationships, search and permissions in
//! one file). The engine is linked from the static library this crate carries; nothing is
//! compiled but this crate's Rust.
//!
//! ```no_run
//! use edbe::Database;
//! let db = Database::open("app.edbe")?;
//! db.put("users:ada", r#"{"name":"Ada","city":"Oslo"}"#)?;
//! let rows = db.query("SELECT name FROM users WHERE city = ?", &["Oslo".into()])?;
//! assert_eq!(rows, r#"[{"name":"Ada"}]"#);
//! # Ok::<(), edbe::Error>(())
//! ```
//!
//! Rows come back as the text of one JSON array (serde_json reads it). A `Database` may be
//! shared between threads; a `Transaction` or a `Statement` stays on the thread that made it.

use std::ffi::{c_char, c_void, CStr, CString};
use std::fmt;
use std::marker::PhantomData;

mod ffi {
    use super::*;

    #[repr(C)]
    pub struct Slice {
        pub data: *const c_void,
        pub length: usize,
    }

    #[repr(C)]
    #[derive(Clone, Copy)]
    pub struct Value {
        pub data: *const c_void,
        pub length: usize,
        pub ty: u32,
    }

    #[repr(C)]
    pub struct Options {
        pub struct_size: u32,
        pub page_size: u32,
        pub durability: i32,
        pub read_only: u8,
        pub create_if_missing: u8,
        pub exclusive: u8,
        pub _pad: u8,
        pub busy_timeout_ms: u32,
        pub max_document_bytes: u64,
        pub max_query_memory: u64,
        pub max_tx_memory: u64,
        pub max_traversal_depth: u32,
        pub max_snapshots: u32,
        pub reserved0: u64,
        pub alloc: *const c_void,
        pub free: *const c_void,
        pub alloc_ctx: *const c_void,
        pub log: *const c_void,
        pub log_ctx: *const c_void,
        pub journal: u8,
        pub replica: u8,
        pub _pad2: [u8; 6],
        pub reserved1: u64,
        pub wal: u8,
        pub _pad3: [u8; 7],
        pub wal_checkpoint_bytes: u64,
        pub mode: u8,
        pub _pad4: [u8; 7],
    }

    extern "C" {
        pub fn db_options_init(o: *mut Options);
        pub fn db_open(path: *const c_char, o: *const Options, out: *mut *mut c_void) -> i32;
        pub fn db_close(db: *mut c_void) -> i32;
        pub fn db_errmsg(db: *const c_void) -> *const c_char;
        pub fn db_errpos(db: *const c_void) -> i32;
        pub fn db_tx_errmsg(tx: *const c_void) -> *const c_char;
        pub fn db_tx_errpos(tx: *const c_void) -> i32;
        pub fn db_status_str(s: i32) -> *const c_char;
        pub fn db_version() -> *const c_char;
        pub fn db_abi_version() -> u32;
        pub fn db_set_label(db: *mut c_void, label: *const c_char) -> i32;
        pub fn db_set_principal(db: *mut c_void, principal: *const c_char) -> i32;
        pub fn db_tx_set_label(tx: *mut c_void, label: *const c_char) -> i32;
        pub fn db_tx_set_principal(tx: *mut c_void, principal: *const c_char) -> i32;
        pub fn db_execute(db: *mut c_void, sql: *const c_char) -> i32;
        pub fn db_prepare(db: *mut c_void, sql: *const c_char, out: *mut *mut c_void) -> i32;
        pub fn db_tx_prepare(tx: *mut c_void, sql: *const c_char, out: *mut *mut c_void) -> i32;
        pub fn db_stmt_run(stmt: *mut c_void, values: *const Value, count: u32, out: *mut Slice) -> i32;
        pub fn db_stmt_run_many(stmt: *mut c_void, params: Slice, out: *mut Slice) -> i32;
        pub fn db_stmt_finalize(stmt: *mut c_void) -> i32;
        pub fn db_begin(db: *mut c_void, mode: i32, out: *mut *mut c_void) -> i32;
        pub fn db_begin_as(db: *mut c_void, mode: i32, label: *const c_char, principal: *const c_char, out: *mut *mut c_void) -> i32;
        pub fn db_tx_execute(tx: *mut c_void, sql: *const c_char) -> i32;
        pub fn db_commit(tx: *mut c_void) -> i32;
        pub fn db_rollback(tx: *mut c_void) -> i32;
    }
}

/// The engine's status codes (`DB_OK`, `DB_ERR_NOT_FOUND`, ... in `edbe.h`); `as i32` gives the
/// number. An [`Error`] carries one of the negative ones.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Hash)]
#[repr(i32)]
pub enum Status {
    Ok = 0,
    Row = 1,
    Done = 2,
    NotFound = -1,
    Exists = -2,
    InvalidArg = -3,
    Syntax = -4,
    Schema = -5,
    NoIndex = -6,
    Conflict = -7,
    Busy = -8,
    ReadOnly = -9,
    Io = -10,
    Corrupt = -11,
    Version = -12,
    NoMem = -13,
    Limit = -14,
    TxState = -15,
    Interrupted = -16,
    Denied = -17,
    Constraint = -18,
    Internal = -99,
}

impl Status {
    const ALL: [Status; 22] = [
        Status::Ok, Status::Row, Status::Done, Status::NotFound, Status::Exists, Status::InvalidArg,
        Status::Syntax, Status::Schema, Status::NoIndex, Status::Conflict, Status::Busy, Status::ReadOnly,
        Status::Io, Status::Corrupt, Status::Version, Status::NoMem, Status::Limit, Status::TxState,
        Status::Interrupted, Status::Denied, Status::Constraint, Status::Internal,
    ];

    /// The status of a code, `None` for a number `edbe.h` does not list.
    pub fn from_code(code: i32) -> Option<Status> {
        Status::ALL.iter().copied().find(|s| *s as i32 == code)
    }

    /// The name `edbe.h` gives it: `"DB_ERR_BUSY"`.
    pub fn name(self) -> String {
        unsafe { text(ffi::db_status_str(self as i32)) }
    }

    // What an error says when the engine gave it no message of its own.
    fn says(self) -> &'static str {
        match self {
            Status::Ok | Status::Row | Status::Done => "",
            Status::NotFound => "not found",
            Status::Exists => "already exists",
            Status::InvalidArg => "invalid argument",
            Status::Syntax => "syntax error",
            Status::Schema => "schema error",
            Status::NoIndex => "no index can answer the statement",
            Status::Conflict => "conflict",
            Status::Busy => "the database is busy: another writer held it past the busy timeout",
            Status::ReadOnly => "the database or transaction is read-only",
            Status::Io => "input/output error",
            Status::Corrupt => "the file is corrupt",
            Status::Version => "the file was written by an incompatible engine",
            Status::NoMem => "out of memory",
            Status::Limit => "a limit was reached",
            Status::TxState => "the transaction cannot go on: roll it back",
            Status::Interrupted => "interrupted",
            Status::Denied => "permission denied",
            Status::Constraint => "a declared constraint refused the write",
            Status::Internal => "internal error",
        }
    }
}

/// An engine error: its status (`-3`, named `DB_ERR_INVALID_ARG`), its message, and the byte
/// offset in the statement text the engine noted for it (`-1` when none).
#[derive(Debug, Clone, PartialEq)]
pub struct Error {
    pub status: i32,
    pub name: String,
    pub message: String,
    pub pos: i32,
}

impl Error {
    /// The status as a [`Status`]; a code this crate does not know (from a newer engine) reads
    /// as [`Status::Internal`], and `status` keeps its number.
    pub fn kind(&self) -> Status {
        Status::from_code(self.status).unwrap_or(Status::Internal)
    }
}

impl fmt::Display for Error {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        if self.message.is_empty() {
            write!(f, "{}", self.name)
        } else {
            write!(f, "{}: {}", self.name, self.message)
        }
    }
}

impl std::error::Error for Error {}

pub type Result<T> = std::result::Result<T, Error>;

/// A parameter bound to a `?` of a statement.
#[derive(Debug, Clone, PartialEq)]
pub enum Value<'a> {
    Null,
    Bool(bool),
    Int(i64),
    UInt(u64),
    Float(f64),
    Text(&'a str),
    Bytes(&'a [u8]),
    /// a JSON document (an object or array) as text
    Json(&'a str),
}

impl From<bool> for Value<'_> { fn from(v: bool) -> Self { Value::Bool(v) } }
impl From<i32> for Value<'_> { fn from(v: i32) -> Self { Value::Int(v as i64) } }
impl From<i64> for Value<'_> { fn from(v: i64) -> Self { Value::Int(v) } }
impl From<u32> for Value<'_> { fn from(v: u32) -> Self { Value::Int(v as i64) } }
impl From<u64> for Value<'_> { fn from(v: u64) -> Self { Value::UInt(v) } }
impl From<f64> for Value<'_> { fn from(v: f64) -> Self { Value::Float(v) } }
impl<'a> From<&'a str> for Value<'a> { fn from(v: &'a str) -> Self { Value::Text(v) } }
impl<'a> From<&'a String> for Value<'a> { fn from(v: &'a String) -> Self { Value::Text(v) } }
impl<'a> From<&'a [u8]> for Value<'a> { fn from(v: &'a [u8]) -> Self { Value::Bytes(v) } }
impl<'a, T: Into<Value<'a>>> From<Option<T>> for Value<'a> {
    fn from(v: Option<T>) -> Self { v.map_or(Value::Null, Into::into) }
}

/// The commit mode of a database: `Default`, `Durable` (also survives a power loss on macOS)
/// or `Fast` (no sync per commit).
#[derive(Debug, Clone, Copy, PartialEq, Eq, Default)]
pub enum Mode {
    #[default]
    Default,
    Durable,
    Fast,
}

/// How [`Database::open_with_options`] opens a file. `Options::default()` holds the engine's
/// own defaults; change the fields you need:
///
/// ```no_run
/// let db = edbe::Database::open_with_options("app.edbe", &edbe::Options {
///     busy_timeout_ms: 200,
///     ..Default::default()
/// })?;
/// # Ok::<(), edbe::Error>(())
/// ```
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct Options {
    /// the commit mode
    pub mode: Mode,
    /// refuse every write
    pub read_only: bool,
    /// create the file when it does not exist (default `true`)
    pub create_if_missing: bool,
    /// refuse other processes while this handle is open
    pub exclusive: bool,
    /// how long a writer waits for another one, in milliseconds (0: fail at once; default 5000)
    pub busy_timeout_ms: u32,
    /// the largest document a write accepts
    pub max_document_bytes: u64,
    /// the memory one query may use
    pub max_query_memory: u64,
    /// the memory one write transaction may hold
    pub max_tx_memory: u64,
    /// the most hops a traversal may take
    pub max_traversal_depth: u32,
    /// the most snapshots (read transactions) open at once
    pub max_snapshots: u32,
    /// record every commit in `<path>.journal` (a file opened `exclusive`)
    pub journal: bool,
    /// in `Mode::Fast`, the length of log that starts a checkpoint
    pub wal_checkpoint_bytes: u64,
}

impl Default for Options {
    fn default() -> Options {
        let o = engine_options();
        Options {
            mode: match o.mode { 2 => Mode::Durable, 3 => Mode::Fast, _ => Mode::Default },
            read_only: o.read_only != 0,
            create_if_missing: o.create_if_missing != 0,
            exclusive: o.exclusive != 0,
            busy_timeout_ms: o.busy_timeout_ms,
            max_document_bytes: o.max_document_bytes,
            max_query_memory: o.max_query_memory,
            max_tx_memory: o.max_tx_memory,
            max_traversal_depth: o.max_traversal_depth,
            max_snapshots: o.max_snapshots,
            journal: o.journal != 0,
            wal_checkpoint_bytes: o.wal_checkpoint_bytes,
        }
    }
}

fn engine_options() -> ffi::Options {
    let mut o = std::mem::MaybeUninit::<ffi::Options>::zeroed();
    unsafe {
        ffi::db_options_init(o.as_mut_ptr());
        o.assume_init()
    }
}

/// The engine's version, e.g. "0.1.0".
pub fn version() -> String {
    unsafe { text(ffi::db_version()) }
}

/// The engine's C ABI version (major, minor).
pub fn abi_version() -> (u32, u32) {
    let v = unsafe { ffi::db_abi_version() };
    (v >> 16, v & 0xffff)
}

/// An open database.
pub struct Database {
    h: *mut c_void,
}

unsafe impl Send for Database {}
unsafe impl Sync for Database {}

impl Database {
    /// Opens or creates a database file; `":memory:"` lives only in this process.
    pub fn open(path: &str) -> Result<Database> {
        Database::open_with_options(path, &Options::default())
    }

    /// Opens with a commit mode, and read-only when `read_only`.
    pub fn open_with(path: &str, mode: Mode, read_only: bool) -> Result<Database> {
        Database::open_with_options(path, &Options { mode, read_only, ..Options::default() })
    }

    /// Opens with every option of [`Options`].
    pub fn open_with_options(path: &str, options: &Options) -> Result<Database> {
        let cpath = cstring(path)?;
        let mut o = engine_options();
        if o.struct_size as usize != std::mem::size_of::<ffi::Options>() {
            return Err(invalid("the engine library does not match this crate"));
        }
        o.mode = match options.mode { Mode::Default => 1, Mode::Durable => 2, Mode::Fast => 3 };
        o.read_only = options.read_only as u8;
        o.create_if_missing = options.create_if_missing as u8;
        o.exclusive = options.exclusive as u8;
        o.busy_timeout_ms = options.busy_timeout_ms;
        o.max_document_bytes = options.max_document_bytes;
        o.max_query_memory = options.max_query_memory;
        o.max_tx_memory = options.max_tx_memory;
        o.max_traversal_depth = options.max_traversal_depth;
        o.max_snapshots = options.max_snapshots;
        o.journal = options.journal as u8;
        o.wal_checkpoint_bytes = options.wal_checkpoint_bytes;
        let mut h = std::ptr::null_mut();
        let st = unsafe { ffi::db_open(cpath.as_ptr(), &o, &mut h) };
        if st < 0 {
            return Err(unsafe { engine_error(st, ffi::db_errmsg(std::ptr::null()), -1, None) });
        }
        Ok(Database { h })
    }

    /// Runs one statement, `?` bound to `params` in order, and returns its rows as the text of
    /// one JSON array.
    pub fn query(&self, sql: &str, params: &[Value]) -> Result<String> {
        self.prepare(sql)?.query(params)
    }

    /// Runs `sql` and discards its rows. With `params` it is run as [`query`](Self::query) is;
    /// without, it may be a script of several statements (`BEGIN; ...; COMMIT;`).
    pub fn execute(&self, sql: &str, params: &[Value]) -> Result<()> {
        if !params.is_empty() {
            return self.prepare(sql)?.execute(params);
        }
        let csql = cstring(sql)?;
        let st = unsafe { ffi::db_execute(self.h, csql.as_ptr()) };
        if st < 0 { Err(self.owner().error(st, None)) } else { Ok(()) }
    }

    /// Prepares `sql` once, to run it many times with [`Statement::query`],
    /// [`Statement::execute`] or [`Statement::run_many`]. Each run is its own transaction.
    pub fn prepare(&self, sql: &str) -> Result<Statement<'_>> {
        self.owner().prepare(sql)
    }

    /// Stores `doc` at the address `key` ("users:ada"), created or replaced. `doc` is JSON
    /// text (a document), or any other bytes, which are stored as they are (as `db_put` does).
    pub fn put(&self, key: &str, doc: impl AsRef<[u8]>) -> Result<()> {
        self.owner().put(key, doc.as_ref())
    }

    /// Reads the record at `key` as JSON text, as `GET` returns it: its `id` and then its
    /// fields. `None` when there is none.
    pub fn get(&self, key: &str) -> Result<Option<String>> {
        self.query("GET ?", &[Value::Text(key)]).map(first)
    }

    /// Removes the record at `key`; `DB_ERR_NOT_FOUND` when there is none.
    pub fn delete(&self, key: &str) -> Result<()> {
        self.query("DELETE ?", &[Value::Text(key)]).map(|_| ())
    }

    /// Begins a transaction, a write transaction when `write`.
    pub fn begin(&self, write: bool) -> Result<Transaction<'_>> {
        let mut tx = std::ptr::null_mut();
        let st = unsafe { ffi::db_begin(self.h, write as i32, &mut tx) };
        if st < 0 {
            return Err(self.owner().error(st, None));
        }
        Ok(Transaction { h: tx, db: self.h, done: false, _db: PhantomData, _thread: PhantomData })
    }

    /// Begins a transaction under a label and a principal (the record key of the acting user,
    /// `"users:42"`), for this transaction only; `None` keeps the handle's own.
    pub fn begin_as(&self, write: bool, label: Option<&str>, principal: Option<&str>) -> Result<Transaction<'_>> {
        let label = label.map(cstring).transpose()?;
        let principal = principal.map(cstring).transpose()?;
        let mut tx = std::ptr::null_mut();
        let st = unsafe {
            ffi::db_begin_as(self.h, write as i32, ptr_or_null(&label), ptr_or_null(&principal), &mut tx)
        };
        if st < 0 {
            return Err(self.owner().error(st, None));
        }
        Ok(Transaction { h: tx, db: self.h, done: false, _db: PhantomData, _thread: PhantomData })
    }

    /// Sets the label of the handle, which every later call on it (from any thread) runs under;
    /// `""` clears it. A host call: it is always permitted, unlike `SET LABEL`.
    pub fn set_label(&self, label: &str) -> Result<()> {
        let c = nonempty(label)?;
        let st = unsafe { ffi::db_set_label(self.h, ptr_or_null(&c)) };
        if st < 0 { Err(self.owner().error(st, None)) } else { Ok(()) }
    }

    /// Sets the principal of the handle (`"users:42"`), which row-level policies read as
    /// `@principal`; `""` clears it.
    pub fn set_principal(&self, principal: &str) -> Result<()> {
        let c = nonempty(principal)?;
        let st = unsafe { ffi::db_set_principal(self.h, ptr_or_null(&c)) };
        if st < 0 { Err(self.owner().error(st, None)) } else { Ok(()) }
    }

    fn owner(&self) -> Owner {
        Owner::Db(self.h)
    }
}

impl Drop for Database {
    fn drop(&mut self) {
        unsafe { ffi::db_close(self.h) };
    }
}

/// A transaction from [`Database::begin`]; rolled back when dropped without `commit`.
pub struct Transaction<'db> {
    h: *mut c_void,
    db: *mut c_void,
    done: bool,
    _db: PhantomData<&'db Database>,
    _thread: PhantomData<*const ()>,
}

impl Transaction<'_> {
    /// [`Database::query`] in the transaction.
    pub fn query(&self, sql: &str, params: &[Value]) -> Result<String> {
        self.prepare(sql)?.query(params)
    }

    /// [`Database::execute`] in the transaction.
    pub fn execute(&self, sql: &str, params: &[Value]) -> Result<()> {
        if !params.is_empty() {
            return self.prepare(sql)?.execute(params);
        }
        let csql = cstring(sql)?;
        let st = unsafe { ffi::db_tx_execute(self.h, csql.as_ptr()) };
        if st < 0 { Err(self.owner().error(st, None)) } else { Ok(()) }
    }

    /// [`Database::prepare`] in the transaction: every run of the statement is part of it, and
    /// the statement cannot outlive it.
    pub fn prepare(&self, sql: &str) -> Result<Statement<'_>> {
        self.owner().prepare(sql)
    }

    /// Stores `doc` at the address `key` in the transaction, created or replaced: JSON text
    /// (a document), or any other bytes, stored as they are. Returns `()`.
    pub fn put(&self, key: &str, doc: impl AsRef<[u8]>) -> Result<()> {
        self.owner().put(key, doc.as_ref())
    }

    /// Reads the record at `key` as the transaction sees it (its own writes included), as JSON
    /// text with its `id` first; `None` when there is none or a policy hides it.
    pub fn get(&self, key: &str) -> Result<Option<String>> {
        self.query("GET ?", &[Value::Text(key)]).map(first)
    }

    /// Removes the record at `key` in the transaction; `DB_ERR_NOT_FOUND` when there is none.
    pub fn delete(&self, key: &str) -> Result<()> {
        self.query("DELETE ?", &[Value::Text(key)]).map(|_| ())
    }

    /// Sets the label of this transaction only; `""` clears it (the transaction then runs as
    /// the system principal, whatever the handle's label).
    pub fn set_label(&self, label: &str) -> Result<()> {
        let c = nonempty(label)?;
        let st = unsafe { ffi::db_tx_set_label(self.h, ptr_or_null(&c)) };
        if st < 0 { Err(self.owner().error(st, None)) } else { Ok(()) }
    }

    /// Sets the principal of this transaction only; `""` clears it.
    pub fn set_principal(&self, principal: &str) -> Result<()> {
        let c = nonempty(principal)?;
        let st = unsafe { ffi::db_tx_set_principal(self.h, ptr_or_null(&c)) };
        if st < 0 { Err(self.owner().error(st, None)) } else { Ok(()) }
    }

    /// Makes the writes durable and ends the transaction.
    pub fn commit(mut self) -> Result<()> {
        self.done = true;
        let st = unsafe { ffi::db_commit(self.h) };
        // (the transaction is gone: the message of a failed commit is the handle's)
        if st < 0 { Err(Owner::Db(self.db).error(st, None)) } else { Ok(()) }
    }

    /// Discards the writes and ends the transaction.
    pub fn rollback(mut self) -> Result<()> {
        self.done = true;
        let st = unsafe { ffi::db_rollback(self.h) };
        if st < 0 { Err(Owner::Db(self.db).error(st, None)) } else { Ok(()) }
    }

    fn owner(&self) -> Owner {
        Owner::Tx(self.h)
    }
}

impl Drop for Transaction<'_> {
    fn drop(&mut self) {
        if !self.done {
            unsafe { ffi::db_rollback(self.h) };
        }
    }
}

/// A prepared statement from [`Database::prepare`] or [`Transaction::prepare`]: parsed once,
/// run any number of times. It borrows what prepared it, and is finalized when dropped. It
/// stays on the thread that prepared it.
pub struct Statement<'a> {
    h: *mut c_void,
    owner: Owner,
    _owner: PhantomData<&'a ()>,
    _thread: PhantomData<*const ()>,
}

impl Statement<'_> {
    /// Runs the statement, `?` bound to `params` in order, and returns its rows as the text of
    /// one JSON array.
    pub fn query(&self, params: &[Value]) -> Result<String> {
        run(self.h, params).map_err(|(st, hint)| self.owner.run_error(st, hint))
    }

    /// Runs the statement and discards its rows.
    pub fn execute(&self, params: &[Value]) -> Result<()> {
        self.query(params).map(|_| ())
    }

    /// Runs the statement once for each list of parameters in `params_json`, the text of a JSON
    /// array of arrays (`[[1,{"name":"Ann"}],[2,{"name":"Bob"}]]`; an object or an array is
    /// bound as a document), in one call, and returns the rows of every run as one JSON array.
    /// A run that fails ends the call with its error; the runs before it stand, unless the
    /// statement belongs to a transaction that is then rolled back.
    pub fn run_many(&self, params_json: &str) -> Result<String> {
        let mut out = ffi::Slice { data: std::ptr::null(), length: 0 };
        let st = unsafe { ffi::db_stmt_run_many(self.h, slice(params_json.as_bytes()), &mut out) };
        if st < 0 {
            return Err(self.owner.error(st, None));
        }
        Ok(rows(&out))
    }
}

impl Drop for Statement<'_> {
    fn drop(&mut self) {
        unsafe { ffi::db_stmt_finalize(self.h) };
    }
}

// What a statement was prepared on: where its errors are read.
#[derive(Clone, Copy)]
enum Owner {
    Db(*mut c_void),
    Tx(*mut c_void),
}

// The start of the message db_stmt_run gives a parameter it cannot read (a Json value that does
// not parse).
const VALUE_REFUSED: &str = "a bound value is malformed";

impl Owner {
    fn prepare<'a>(self, sql: &str) -> Result<Statement<'a>> {
        let csql = cstring(sql)?;
        let mut stmt = std::ptr::null_mut();
        let st = unsafe {
            match self {
                Owner::Db(h) => ffi::db_prepare(h, csql.as_ptr(), &mut stmt),
                Owner::Tx(h) => ffi::db_tx_prepare(h, csql.as_ptr(), &mut stmt),
            }
        };
        if st < 0 {
            return Err(self.error(st, None));
        }
        Ok(Statement { h: stmt, owner: self, _owner: PhantomData, _thread: PhantomData })
    }

    fn error(self, st: i32, hint: Option<&str>) -> Error {
        unsafe {
            match self {
                Owner::Db(h) => engine_error(st, ffi::db_errmsg(h), ffi::db_errpos(h), hint),
                Owner::Tx(h) => engine_error(st, ffi::db_tx_errmsg(h), ffi::db_tx_errpos(h), hint),
            }
        }
    }

    // put: stores what db_put stores (JSON text or an encoded document as a document, anything
    // else as bytes), through a PUT statement: so that it runs inside a BEGIN a script left open
    // on the handle, as get and delete do, and says what a PUT statement says when refused.
    fn put(self, key: &str, doc: &[u8]) -> Result<()> {
        let put = self.prepare("PUT ? ?")?;
        let key = raw(key.as_bytes(), TYPE_STRING);
        if let Err((st, _)) = run_values(put.h, &[key, raw(doc, TYPE_JSON)]) {
            let e = self.run_error(st, None);
            if !(e.kind() == Status::InvalidArg && e.message.starts_with(VALUE_REFUSED)) {
                return Err(e);
            }
            run_values(put.h, &[key, raw(doc, TYPE_BYTES)]).map_err(|(st, _)| self.run_error(st, None))?;
        }
        Ok(())
    }

    // The error of a db_stmt_run.
    fn run_error(self, st: i32, hint: Option<&str>) -> Error {
        self.error(st, hint)
    }
}

fn run(stmt: *mut c_void, params: &[Value]) -> std::result::Result<String, (i32, Option<&'static str>)> {
    let mut scratch: Vec<[u8; 8]> = Vec::with_capacity(params.len());
    for p in params {
        match p {
            Value::Bool(b) => scratch.push([*b as u8, 0, 0, 0, 0, 0, 0, 0]),
            Value::Int(n) => scratch.push(n.to_le_bytes()),
            Value::UInt(n) => scratch.push(n.to_le_bytes()),
            Value::Float(f) => scratch.push(f.to_le_bytes()),
            _ => {}
        }
    }
    let mut next = scratch.iter();
    let values: Vec<ffi::Value> = params
        .iter()
        .map(|p| match p {
            Value::Null => raw(&[], TYPE_NULL),
            Value::Bool(_) => raw(&next.next().unwrap()[..1], TYPE_BOOL),
            Value::Int(_) => raw(next.next().unwrap(), TYPE_INT64),
            Value::UInt(_) => raw(next.next().unwrap(), TYPE_UINT64),
            Value::Float(_) => raw(next.next().unwrap(), TYPE_FLOAT64),
            Value::Text(s) => raw(s.as_bytes(), TYPE_STRING),
            Value::Bytes(b) => raw(b, TYPE_BYTES),
            Value::Json(s) => raw(s.as_bytes(), TYPE_JSON),
        })
        .collect();
    run_values(stmt, &values)
}

fn run_values(stmt: *mut c_void, values: &[ffi::Value]) -> std::result::Result<String, (i32, Option<&'static str>)> {
    let mut out = ffi::Slice { data: std::ptr::null(), length: 0 };
    let st = unsafe { ffi::db_stmt_run(stmt, values.as_ptr(), values.len() as u32, &mut out) };
    if st < 0 {
        return Err((st, None));
    }
    Ok(rows(&out))
}

// DB_Type
const TYPE_NULL: u32 = 0;
const TYPE_BOOL: u32 = 1;
const TYPE_INT64: u32 = 2;
const TYPE_UINT64: u32 = 3;
const TYPE_FLOAT64: u32 = 4;
const TYPE_STRING: u32 = 5;
const TYPE_BYTES: u32 = 6;
const TYPE_JSON: u32 = 7;

fn raw(b: &[u8], ty: u32) -> ffi::Value {
    let data = if ty == TYPE_NULL { std::ptr::null() } else { b.as_ptr().cast() };
    ffi::Value { data, length: b.len(), ty }
}

fn rows(out: &ffi::Slice) -> String {
    if out.data.is_null() {
        return "[]".to_string();
    }
    let bytes = unsafe { std::slice::from_raw_parts(out.data as *const u8, out.length) };
    String::from_utf8_lossy(bytes).into_owned()
}

fn first(rows: String) -> Option<String> {
    if rows.len() <= 2 { None } else { Some(rows[1..rows.len() - 1].to_string()) }
}

fn slice(b: &[u8]) -> ffi::Slice {
    ffi::Slice { data: b.as_ptr().cast(), length: b.len() }
}

fn cstring(s: &str) -> Result<CString> {
    CString::new(s).map_err(|_| invalid("text holds a NUL byte"))
}

fn nonempty(s: &str) -> Result<Option<CString>> {
    if s.is_empty() { Ok(None) } else { cstring(s).map(Some) }
}

fn ptr_or_null(c: &Option<CString>) -> *const c_char {
    c.as_ref().map_or(std::ptr::null(), |c| c.as_ptr())
}

fn invalid(msg: &str) -> Error {
    Error { status: Status::InvalidArg as i32, name: "DB_ERR_INVALID_ARG".into(), message: msg.into(), pos: -1 }
}

unsafe fn text(p: *const c_char) -> String {
    if p.is_null() { String::new() } else { CStr::from_ptr(p).to_string_lossy().into_owned() }
}

// An error from the engine's status, message and position. A failure the engine gives no message
// for says what its status means (`hint` when the caller knows better).
unsafe fn engine_error(st: i32, msg: *const c_char, pos: i32, hint: Option<&str>) -> Error {
    let mut message = text(msg);
    if message.is_empty() {
        message = match (hint, Status::from_code(st)) {
            (Some(h), _) => h.to_string(),
            (None, Some(s)) => s.says().to_string(),
            (None, None) => String::new(),
        };
    }
    Error { status: st, name: text(ffi::db_status_str(st)), message, pos }
}
