# Schema at 7b31d01

The last `schema.sql` before migrations, as `git show 7b31d01:internal/definition/repository/schema.sql` prints it. A dev build from 7c21646 up to 7b31d01 created a database from it, at every start. `tests/control` (migration_test.go) adopts a database built from it at version 2, and compares the schema migrations produce with it (#292).
