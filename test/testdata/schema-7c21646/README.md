# Schema at 7c21646

The control service's schema as the published 7c216469476d created it, as `git show 7c21646:internal/definition/repository/schema.sql` prints it. `tests/control` (migration_test.go) builds a database from it, seeds rows the way that release wrote them, and starts the current binary on it (#292). It is kept apart from migration 1, which repeats it, so a mistake in that migration is not also in what it is tested against.
