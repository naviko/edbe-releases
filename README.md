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

- **One file, no server.** Nothing to install, configure or keep running beside your app.
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

## Downloads

Each [release](../../releases) has an archive per platform:

| Platform | Archive |
| --- | --- |
| Linux x64 | `edbe-<version>-linux_amd64.tar.gz` |
| Linux ARM64 | `edbe-<version>-linux_arm64.tar.gz` |
| macOS Apple silicon | `edbe-<version>-darwin_arm64.tar.gz` |
| macOS Intel | `edbe-<version>-darwin_amd64.tar.gz` |

Every archive holds the static library (`libedbe.a`), the header (`edbe.h`) and the license.
The archive for the platform a release was built on also holds the shared library and the
`edbe` command-line tool. Check downloads against `SHA256SUMS`.

## License

Copyright (c) 2026 naviko. [Elastic License 2.0](LICENSE).
