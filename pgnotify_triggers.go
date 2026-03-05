package arcana

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// TriggerExec can execute DDL statements. *pgxpool.Pool satisfies this.
type TriggerExec interface {
	Exec(ctx context.Context, sql string, args ...any) (any, error)
}

var validIdentifier = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

func sanitizeIdentifier(name string) (string, error) {
	if !validIdentifier.MatchString(name) {
		return "", fmt.Errorf("arcana: invalid SQL identifier: %q", name)
	}
	return name, nil
}

// GenerateTriggerSQL generates CREATE OR REPLACE FUNCTION + DROP/CREATE TRIGGER
// SQL for each table in the tables map (table_name -> []tracked_columns).
// channel is the pg_notify channel name (e.g. "arcana_changes").
// Returns a slice of SQL statements (3 per table: function + drop trigger + create trigger).
func GenerateTriggerSQL(channel string, tables map[string][]string) []string {
	tableNames := make([]string, 0, len(tables))
	for t := range tables {
		tableNames = append(tableNames, t)
	}
	sort.Strings(tableNames)

	var stmts []string
	for _, table := range tableNames {
		cols := tables[table]
		stmts = append(stmts, generateTriggerStmts(channel, table, cols)...)
	}
	return stmts
}

// EnsureTriggers creates pg_notify triggers for all registered tables.
func EnsureTriggers(ctx context.Context, exec TriggerExec, channel string, tables map[string][]string) error {
	if _, err := sanitizeIdentifier(channel); err != nil {
		return err
	}
	for table, cols := range tables {
		if _, err := sanitizeIdentifier(table); err != nil {
			return err
		}
		for _, col := range cols {
			if _, err := sanitizeIdentifier(col); err != nil {
				return err
			}
		}
	}
	stmts := GenerateTriggerSQL(channel, tables)
	for _, sql := range stmts {
		if _, err := exec.Exec(ctx, sql); err != nil {
			return fmt.Errorf("arcana: ensure triggers: %w", err)
		}
	}
	return nil
}

func generateTriggerStmts(channel, table string, cols []string) []string {
	funcName := "arcana_notify_" + table
	triggerName := "trg_arcana_" + table

	fn := generateFunction(channel, table, funcName, cols)
	drop := fmt.Sprintf("DROP TRIGGER IF EXISTS %s ON %s", triggerName, table)
	create := fmt.Sprintf(
		"CREATE TRIGGER %s AFTER INSERT OR UPDATE OR DELETE ON %s FOR EACH ROW EXECUTE FUNCTION %s()",
		triggerName, table, funcName,
	)

	return []string{fn, drop, create}
}

func generateFunction(channel, table, funcName string, cols []string) string {
	var b strings.Builder

	b.WriteString(fmt.Sprintf("CREATE OR REPLACE FUNCTION %s() RETURNS trigger AS $$ ", funcName))
	b.WriteString("DECLARE payload TEXT; ")
	if len(cols) > 0 {
		b.WriteString("changed_cols TEXT; ")
	}
	b.WriteString("BEGIN ")

	// INSERT
	b.WriteString("IF TG_OP = 'INSERT' THEN ")
	b.WriteString(fmt.Sprintf(
		`payload := '{"table":"%s","id":"' || NEW.id::text || '","op":"INSERT"}'; `,
		table,
	))
	b.WriteString(fmt.Sprintf("PERFORM pg_notify('%s', payload); ", channel))
	b.WriteString("RETURN NEW; ")
	b.WriteString("END IF; ")

	// DELETE
	b.WriteString("IF TG_OP = 'DELETE' THEN ")
	b.WriteString(fmt.Sprintf(
		`payload := '{"table":"%s","id":"' || OLD.id::text || '","op":"DELETE"}'; `,
		table,
	))
	b.WriteString(fmt.Sprintf("PERFORM pg_notify('%s', payload); ", channel))
	b.WriteString("RETURN OLD; ")
	b.WriteString("END IF; ")

	// UPDATE
	b.WriteString("IF TG_OP = 'UPDATE' THEN ")

	if len(cols) == 0 {
		b.WriteString(fmt.Sprintf(
			`payload := '{"table":"%s","id":"' || NEW.id::text || '","op":"UPDATE"}'; `,
			table,
		))
		b.WriteString(fmt.Sprintf("PERFORM pg_notify('%s', payload); ", channel))
	} else {
		b.WriteString("changed_cols := ''; ")

		for i, col := range cols {
			if i > 0 {
				b.WriteString(fmt.Sprintf(
					`IF NEW.%s IS DISTINCT FROM OLD.%s THEN IF changed_cols != '' THEN changed_cols := changed_cols || ','; END IF; changed_cols := changed_cols || '"%s"'; END IF; `,
					col, col, col,
				))
			} else {
				b.WriteString(fmt.Sprintf(
					`IF NEW.%s IS DISTINCT FROM OLD.%s THEN changed_cols := changed_cols || '"%s"'; END IF; `,
					col, col, col,
				))
			}
		}

		b.WriteString("IF changed_cols = '' THEN RETURN NEW; END IF; ")

		b.WriteString(fmt.Sprintf(
			`payload := '{"table":"%s","id":"' || NEW.id::text || '","op":"UPDATE","columns":[' || changed_cols || ']}'; `,
			table,
		))

		b.WriteString(fmt.Sprintf(
			"IF length(payload) > 7500 THEN payload := '{\"table\":\"%s\",\"id\":\"' || NEW.id::text || '\",\"op\":\"UPDATE\"}'; END IF; ",
			table,
		))

		b.WriteString(fmt.Sprintf("PERFORM pg_notify('%s', payload); ", channel))
	}

	b.WriteString("RETURN NEW; ")
	b.WriteString("END IF; ")

	b.WriteString("RETURN NULL; ")
	b.WriteString("END; $$ LANGUAGE plpgsql")

	return b.String()
}
