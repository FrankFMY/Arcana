package arcana

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pglogrepl"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgproto3"
)

// WALListener implements ChangeDetector using PostgreSQL logical replication.
// Requires wal_level=logical on the PostgreSQL server.
type WALListener struct {
	connString  string
	slotName    string
	publication string
	tables      []string
	logger      *slog.Logger
	handler     func(Change)
	cancel      context.CancelFunc
	conn        *pgconn.PgConn

	// relation cache: OID → table name and column names
	relations map[uint32]relationInfo

	standbyTimeout time.Duration
}

type relationInfo struct {
	Name    string
	Columns []string
}

// WALConfig configures the WALListener.
type WALConfig struct {
	// ConnString is the PostgreSQL connection string.
	// Must NOT include replication=database — it will be added automatically.
	ConnString string

	// SlotName is the replication slot name. Default: "arcana_slot".
	SlotName string

	// Publication is the publication name. Default: "arcana_pub".
	Publication string

	// Tables to replicate. If empty, all tables in the publication are used.
	Tables []string

	// Logger for structured logging.
	Logger *slog.Logger

	// StandbyTimeout is how often to send standby status updates. Default: 10s.
	StandbyTimeout time.Duration
}

// NewWALListener creates a WAL-based change detector.
func NewWALListener(cfg WALConfig) *WALListener {
	slotName := cfg.SlotName
	if slotName == "" {
		slotName = "arcana_slot"
	}
	publication := cfg.Publication
	if publication == "" {
		publication = "arcana_pub"
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	standbyTimeout := cfg.StandbyTimeout
	if standbyTimeout == 0 {
		standbyTimeout = 10 * time.Second
	}

	return &WALListener{
		connString:     cfg.ConnString,
		slotName:       slotName,
		publication:    publication,
		tables:         cfg.Tables,
		logger:         logger,
		relations:      make(map[uint32]relationInfo),
		standbyTimeout: standbyTimeout,
	}
}

// Start begins listening for WAL changes.
func (l *WALListener) Start(ctx context.Context, handler func(Change)) error {
	l.handler = handler

	// Append replication=database to connection string
	connStr := l.connString
	if !strings.Contains(connStr, "replication=") {
		sep := "?"
		if strings.Contains(connStr, "?") {
			sep = "&"
		}
		connStr += sep + "replication=database"
	}

	conn, err := pgconn.Connect(ctx, connStr)
	if err != nil {
		return fmt.Errorf("arcana: WAL connect: %w", err)
	}
	l.conn = conn

	// Create publication if it doesn't exist
	if err := l.ensurePublication(ctx); err != nil {
		conn.Close(ctx)
		return fmt.Errorf("arcana: WAL create publication: %w", err)
	}

	// Create replication slot if it doesn't exist
	startLSN, err := l.ensureReplicationSlot(ctx)
	if err != nil {
		conn.Close(ctx)
		return fmt.Errorf("arcana: WAL create slot: %w", err)
	}

	listenCtx, cancel := context.WithCancel(ctx)
	l.cancel = cancel

	// Start replication
	if err := pglogrepl.StartReplication(listenCtx, conn, l.slotName, startLSN,
		pglogrepl.StartReplicationOptions{
			PluginArgs: []string{
				"proto_version '2'",
				fmt.Sprintf("publication_names '%s'", l.publication),
				"messages 'true'",
			},
		},
	); err != nil {
		cancel()
		conn.Close(ctx)
		return fmt.Errorf("arcana: WAL start replication: %w", err)
	}

	l.logger.Info("WAL listener started",
		"slot", l.slotName,
		"publication", l.publication,
		"start_lsn", startLSN,
	)

	go l.loop(listenCtx, startLSN)
	return nil
}

// Stop terminates the WAL listener.
func (l *WALListener) Stop() error {
	if l.cancel != nil {
		l.cancel()
	}
	if l.conn != nil {
		return l.conn.Close(context.Background())
	}
	return nil
}

func (l *WALListener) ensurePublication(ctx context.Context) error {
	tables := "*"
	if len(l.tables) > 0 {
		tables = strings.Join(l.tables, ", ")
	}

	sql := fmt.Sprintf(
		"SELECT 1 FROM pg_publication WHERE pubname = '%s'",
		l.publication,
	)
	result := l.conn.Exec(ctx, sql)
	_, err := result.ReadAll()
	if err != nil {
		return err
	}

	// Try to create; ignore "already exists" errors
	createSQL := fmt.Sprintf(
		"CREATE PUBLICATION %s FOR TABLE %s",
		l.publication, tables,
	)
	result = l.conn.Exec(ctx, createSQL)
	_, err = result.ReadAll()
	if err != nil && !strings.Contains(err.Error(), "already exists") {
		return err
	}

	return nil
}

func (l *WALListener) ensureReplicationSlot(ctx context.Context) (pglogrepl.LSN, error) {
	result, err := pglogrepl.CreateReplicationSlot(ctx, l.conn, l.slotName, "pgoutput",
		pglogrepl.CreateReplicationSlotOptions{
			Temporary: false,
			Mode:      pglogrepl.LogicalReplication,
		},
	)
	if err != nil {
		if strings.Contains(err.Error(), "already exists") {
			// Slot exists, start from 0 (will resume from confirmed LSN)
			return 0, nil
		}
		return 0, err
	}

	lsn, _ := pglogrepl.ParseLSN(result.ConsistentPoint)
	return lsn, nil
}

func (l *WALListener) loop(ctx context.Context, startLSN pglogrepl.LSN) {
	clientXLogPos := startLSN
	nextStandby := time.Now().Add(l.standbyTimeout)

	for {
		if time.Now().After(nextStandby) {
			err := pglogrepl.SendStandbyStatusUpdate(ctx, l.conn, pglogrepl.StandbyStatusUpdate{
				WALWritePosition: clientXLogPos,
			})
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				l.logger.Error("WAL standby status update failed", "error", err)
			}
			nextStandby = time.Now().Add(l.standbyTimeout)
		}

		receiveCtx, receiveCancel := context.WithDeadline(ctx, nextStandby)
		rawMsg, err := l.conn.ReceiveMessage(receiveCtx)
		receiveCancel()

		if err != nil {
			if ctx.Err() != nil {
				return
			}
			if pgconn.Timeout(err) {
				continue
			}
			l.logger.Error("WAL receive error", "error", err)
			return
		}

		if errMsg, ok := rawMsg.(*pgproto3.ErrorResponse); ok {
			l.logger.Error("WAL error response",
				"severity", errMsg.Severity,
				"message", errMsg.Message,
				"code", errMsg.Code,
			)
			continue
		}

		msg, ok := rawMsg.(*pgproto3.CopyData)
		if !ok {
			continue
		}

		switch msg.Data[0] {
		case pglogrepl.XLogDataByteID:
			xld, err := pglogrepl.ParseXLogData(msg.Data[1:])
			if err != nil {
				l.logger.Error("WAL parse xlog data", "error", err)
				continue
			}

			l.processWALData(xld.WALData)

			if xld.WALStart > 0 {
				clientXLogPos = xld.WALStart + pglogrepl.LSN(len(xld.WALData))
			}

		case pglogrepl.PrimaryKeepaliveMessageByteID:
			pkm, err := pglogrepl.ParsePrimaryKeepaliveMessage(msg.Data[1:])
			if err != nil {
				l.logger.Error("WAL parse keepalive", "error", err)
				continue
			}
			if pkm.ReplyRequested {
				nextStandby = time.Time{} // send immediately
			}
		}
	}
}

func (l *WALListener) processWALData(walData []byte) {
	logicalMsg, err := pglogrepl.ParseV2(walData, false)
	if err != nil {
		l.logger.Error("WAL parse logical message", "error", err)
		return
	}

	switch msg := logicalMsg.(type) {
	case *pglogrepl.RelationMessageV2:
		cols := make([]string, len(msg.Columns))
		for i, c := range msg.Columns {
			cols[i] = c.Name
		}
		l.relations[msg.RelationID] = relationInfo{
			Name:    msg.RelationName,
			Columns: cols,
		}

	case *pglogrepl.InsertMessageV2:
		rel, ok := l.relations[msg.RelationID]
		if !ok {
			return
		}
		id := l.extractID(msg.Tuple, rel)
		if l.handler != nil {
			l.handler(Change{
				Table:   rel.Name,
				RowID:   id,
				Op:      "INSERT",
				Columns: rel.Columns,
			})
		}

	case *pglogrepl.UpdateMessageV2:
		rel, ok := l.relations[msg.RelationID]
		if !ok {
			return
		}
		id := l.extractID(msg.NewTuple, rel)
		columns := l.changedColumns(msg.OldTuple, msg.NewTuple, rel)
		if l.handler != nil {
			l.handler(Change{
				Table:   rel.Name,
				RowID:   id,
				Op:      "UPDATE",
				Columns: columns,
			})
		}

	case *pglogrepl.DeleteMessageV2:
		rel, ok := l.relations[msg.RelationID]
		if !ok {
			return
		}
		id := l.extractID(msg.OldTuple, rel)
		if l.handler != nil {
			l.handler(Change{
				Table:   rel.Name,
				RowID:   id,
				Op:      "DELETE",
				Columns: rel.Columns,
			})
		}
	}
}

func (l *WALListener) extractID(tuple *pglogrepl.TupleData, rel relationInfo) string {
	if tuple == nil || len(tuple.Columns) == 0 {
		return ""
	}
	// First column is typically the ID (UUID or serial)
	for i, col := range rel.Columns {
		if col == "id" && i < len(tuple.Columns) {
			if tuple.Columns[i].DataType == pglogrepl.TupleDataTypeText {
				return string(tuple.Columns[i].Data)
			}
		}
	}
	// Fallback: use first column
	if len(tuple.Columns) > 0 && tuple.Columns[0].DataType == pglogrepl.TupleDataTypeText {
		return string(tuple.Columns[0].Data)
	}
	return ""
}

func (l *WALListener) changedColumns(oldTuple, newTuple *pglogrepl.TupleData, rel relationInfo) []string {
	if oldTuple == nil {
		return rel.Columns
	}

	var changed []string
	for i, col := range rel.Columns {
		if i >= len(newTuple.Columns) || i >= len(oldTuple.Columns) {
			changed = append(changed, col)
			continue
		}
		oldVal := oldTuple.Columns[i]
		newVal := newTuple.Columns[i]
		if oldVal.DataType != newVal.DataType || string(oldVal.Data) != string(newVal.Data) {
			changed = append(changed, col)
		}
	}
	return changed
}
