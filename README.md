# edbe

An embedded database for applications: documents, relationships, search and permissions in
one small library, with short syntax for each. Your application opens a file. There is no
server to run.

```sql
PUT users:ada {"name":"Ada","address":{"city":"Oslo"}};
SELECT name FROM users WHERE address.city = 'Oslo';
SELECT name FROM users:ada->follows DEPTH 3;
SELECT title FROM articles WHERE match(body, 'deploy') ORDER BY rank() DESC;
GRANT SELECT ON notes TO app WHERE owner = @principal.id;
```

## Why edbe

- **Embedded database.** Nothing to install, configure or keep running beside your app.
- **One database instead of several.** Documents, a graph, full-text and vector search, and
  access control, where you would otherwise combine a database, a search engine and
  application code.
- **Short queries.** Nested fields, arrays, relationships and search read as plain SQL, with
  no JSON functions, recursive queries or side tables.
- **Safe by default.** ACID transactions, snapshot reads, and a file that survives crashes
  and power loss.
- **Access rules in the database.** Row- and field-level permissions apply to every query,
  whoever wrote it.

## Features

- JSON documents and key-value records; nested fields, arrays, partial updates
- Queries: filters, joins, GROUP BY, subqueries, sorting and paging
- Optional schemas: types, NOT NULL, UNIQUE, CHECK, defaults
- References between records with ON DELETE rules
- Graph edges and traversals over many hops
- Full-text search with ranking, typo-tolerant matching, vector search
- Roles, field-level grants and row-level policies
- Many readers and one writer, across threads and processes; readers never wait
- Durability modes: default, durable, fast
- Online backups, compaction, integrity check, a commit journal for replicas
- A stable C API (`edbe.h`)

## Install

Download the file for your language and platform from the [releases](../../releases).
`<platform>` is `darwin_arm64` (macOS Apple silicon), `darwin_amd64` (macOS Intel),
`linux_amd64` (Linux x64) or `linux_arm64` (Linux ARM64).

### Node.js / Bun

```sh
npm install edbe-node-0.1.1-<platform>.tar.gz
```

```js
const { Database } = require("edbe");
const db = new Database("app.edbe");
db.put("users:ada", { name: "Ada", city: "Oslo" });
db.query("SELECT name FROM users WHERE city = ?", ["Oslo"]);   // [ { name: "Ada" } ]
```

### Python

```sh
pip install edbe-python-0.1.1-<platform>.tar.gz
```

```python
from edbe import Database
db = Database("app.edbe")
db.put("users:ada", {"name": "Ada", "city": "Oslo"})
list(db.query("SELECT name FROM users WHERE city = ?", "Oslo"))  # [{'name': 'Ada'}]
```

### .NET

```sh
tar xzf edbe-dotnet-0.1.1-<platform>.tar.gz
dotnet add package Edbe --source edbe-dotnet-0.1.1-<platform>
```

```csharp
using Edbe;
using var db = new Database("app.edbe");
db.Put("users:ada", new { name = "Ada", city = "Oslo" });
db.Query("SELECT name FROM users WHERE city = ?", "Oslo");      // [ { "name": "Ada" } ]
```

### C / C++

```sh
tar xzf edbe-0.1.1-<platform>.tar.gz
c++ -std=c++17 app.cpp -Iedbe-0.1.1-<platform> edbe-0.1.1-<platform>/libedbe.a -pthread -o app
```

```cpp
#include "edbe.hpp"
#include <iostream>

int main() {
    edbe::Database db("app.edbe");
    db.put("users:ada", R"({"name":"Ada","city":"Oslo"})");
    for (auto& row : db.query("SELECT name FROM users WHERE city = 'Oslo'"))
        std::cout << row << "\n";                                       // {"name":"Ada"}
}
```

### Go

```sh
go get github.com/naviko/edbe-releases/go@v0.1.1
```

```go
import edbe "github.com/naviko/edbe-releases/go"

db, err := edbe.Open("app.edbe")
db.Put("users:ada", map[string]any{"name": "Ada", "city": "Oslo"})
db.Query("SELECT name FROM users WHERE city = ?", "Oslo")   // [map[name:Ada]]
```

### Rust

```toml
[dependencies]
edbe = { git = "https://github.com/naviko/edbe-releases", tag = "v0.1.1" }
```

```rust
use edbe::Database;

let db = Database::open("app.edbe")?;
db.put("users:ada", r#"{"name":"Ada","city":"Oslo"}"#)?;
db.query("SELECT name FROM users WHERE city = ?", &["Oslo".into()])?;   // [{"name":"Ada"}]
```

### Odin

```sh
tar xzf edbe-odin-0.1.1-<platform>.tar.gz    # the package folder edbe/: put it beside your code
```

```odin
import edbe "edbe"

db, _ := edbe.open("app.edbe")
edbe.put(db, "users:ada", `{"name":"Ada","city":"Oslo"}`)
edbe.query(db, "SELECT name FROM users WHERE city = ?", "Oslo")   // [{"name":"Ada"}]
```

## License

Copyright (c) 2026 naviko. [Elastic License 2.0](LICENSE).
