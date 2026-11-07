package importer

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// sink is where a stage writes. Rows go in batches through COPY.
type sink interface {
	copy(ctx context.Context, table string, cols []string, rows [][]any) error
	exec(ctx context.Context, sql string, args ...any) error
}

type pgSink struct{ tx pgx.Tx }

func (s *pgSink) copy(ctx context.Context, table string, cols []string, rows [][]any) error {
	if len(rows) == 0 {
		return nil
	}
	_, err := s.tx.CopyFrom(ctx, pgx.Identifier{table}, cols, pgx.CopyFromRows(rows))
	return err
}

func (s *pgSink) exec(ctx context.Context, sql string, args ...any) error {
	_, err := s.tx.Exec(ctx, sql, args...)
	return err
}

// dry writes nothing; the stages count what they would have written.
type dry struct{}

func (dry) copy(context.Context, string, []string, [][]any) error { return nil }
func (dry) exec(context.Context, string, ...any) error            { return nil }

// batch buffers rows for one table and flushes every batchSize rows.
type batch struct {
	w     sink
	table string
	cols  []string
	rows  [][]any
	n     int
}

const batchSize = 5000

func newBatch(w sink, table string, cols ...string) *batch {
	return &batch{w: w, table: table, cols: cols}
}

func (b *batch) add(ctx context.Context, row ...any) error {
	b.rows = append(b.rows, row)
	b.n++
	if len(b.rows) >= batchSize {
		return b.flush(ctx)
	}
	return nil
}

func (b *batch) flush(ctx context.Context) error {
	err := b.w.copy(ctx, b.table, b.cols, b.rows)
	b.rows = b.rows[:0]
	return err
}
