package cmd

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/lib/pq"
	"github.com/mayswind/ezbookkeeping/pkg/core"
	"github.com/mayswind/ezbookkeeping/pkg/datastore"
	"github.com/mayswind/ezbookkeeping/pkg/hengcai"
	"github.com/mayswind/ezbookkeeping/pkg/log"
	"github.com/urfave/cli/v3"
	"xorm.io/xorm"
)

// MigrateHengcai is intentionally an explicit, one-time command. It never
// connects to a remote repository and never modifies the source PostgreSQL
// database; --dry-run can be used to inspect row counts before writing.
var MigrateHengcai = &cli.Command{
	Name:   "migrate-hengcai",
	Usage:  "Migrate the recovered Hengcai PostgreSQL data into ezBookkeeping",
	Action: bindAction(migrateHengcai),
	Flags: []cli.Flag{
		&cli.StringFlag{Name: "source-dsn", Required: true, Usage: "PostgreSQL DSN of the old private backend"},
		&cli.Int64Flag{Name: "uid", Required: true, Usage: "Target ezBookkeeping user id"},
		&cli.StringFlag{Name: "source-user-id", Required: true, Usage: "UUID of the old backend user to migrate"},
		&cli.Int64Flag{Name: "account-id", Usage: "Target credit-card/account id for imported statements"},
		&cli.BoolFlag{Name: "dry-run", Usage: "Only count source rows; do not write target rows"},
	},
}

type migrationMaps struct {
	InvestmentAccounts map[string]int64
	Instruments        map[string]int64
	Purchases          map[string]int64
	Statements         map[string]int64
}

func migrateHengcai(c *core.CliContext) error {
	if c.Int64("uid") <= 0 {
		return fmt.Errorf("--uid must be a positive ezBookkeeping user id")
	}
	config, err := initializeSystem(c)
	if err != nil {
		return err
	}
	if err = updateAllDatabaseTablesStructure(c); err != nil {
		return err
	}
	source, err := sql.Open("postgres", c.String("source-dsn"))
	if err != nil {
		return fmt.Errorf("open source postgres: %w", err)
	}
	defer source.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	if err := source.PingContext(ctx); err != nil {
		return fmt.Errorf("ping source postgres: %w", err)
	}

	maps := migrationMaps{InvestmentAccounts: map[string]int64{}, Instruments: map[string]int64{}, Purchases: map[string]int64{}, Statements: map[string]int64{}}
	dryRun := c.Bool("dry-run")
	counts := map[string]int{}
	if counts["investment_accounts"], err = migrateInvestmentAccounts(ctx, source, c, maps, dryRun); err != nil {
		return err
	}
	if counts["investment_instruments"], err = migrateInstruments(ctx, source, c, maps, dryRun); err != nil {
		return err
	}
	if counts["investment_transactions"], err = migrateInvestmentTransactions(ctx, source, c, maps, dryRun); err != nil {
		return err
	}
	if counts["capex_purchases"], err = migrateCapex(ctx, source, c, maps, dryRun); err != nil {
		return err
	}
	if counts["capex_installments"], err = migrateCapexInstallments(ctx, source, c, maps, dryRun); err != nil {
		return err
	}
	if counts["monthly_projections"], err = migrateProjections(ctx, source, c, dryRun); err != nil {
		return err
	}
	if c.Int64("account-id") > 0 {
		if counts["account_statements"], err = migrateStatements(ctx, source, c, maps, dryRun); err != nil {
			return err
		}
		if counts["statement_lines"], err = migrateStatementLines(ctx, source, c, maps, dryRun); err != nil {
			return err
		}
	}
	encoded, _ := json.Marshal(counts)
	log.CliInfof(c, "[migrate_hengcai] migration completed (dry_run=%t): %s", dryRun, encoded)
	_ = config
	return nil
}

func targetSession(c *core.CliContext, uid int64) *xorm.Session {
	return datastore.Container.UserDataStore.Query(c, uid)
}

func migrateInvestmentAccounts(ctx context.Context, db *sql.DB, c *core.CliContext, m migrationMaps, dry bool) (int, error) {
	rows, err := db.QueryContext(ctx, `SELECT id::text, name, COALESCE(institution,''), account_type, base_currency, is_active, EXTRACT(EPOCH FROM created_at)::bigint FROM investment_accounts WHERE user_id = $1 AND deleted_at IS NULL ORDER BY created_at`, c.String("source-user-id"))
	if err != nil {
		return 0, missingTable("investment_accounts", err)
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var oldID, name, institution, typ, currency string
		var active bool
		var created int64
		if err := rows.Scan(&oldID, &name, &institution, &typ, &currency, &active, &created); err != nil {
			return count, err
		}
		count++
		if dry {
			continue
		}
		item := &hengcai.InvestmentAccount{Uid: c.Int64("uid"), Name: name, Institution: institution, AccountType: typ, BaseCurrency: strings.ToUpper(currency), Active: active, CreatedUnixTime: created}
		if _, err := targetSession(c, item.Uid).Insert(item); err != nil {
			return count, err
		}
		m.InvestmentAccounts[oldID] = item.Id
	}
	return count, rows.Err()
}

func migrateInstruments(ctx context.Context, db *sql.DB, c *core.CliContext, m migrationMaps, dry bool) (int, error) {
	rows, err := db.QueryContext(ctx, `SELECT id::text, asset_type, market, symbol, name, currency, contract_key, price_scale, quantity_scale, is_active FROM investment_instruments WHERE is_active ORDER BY created_at`)
	if err != nil {
		return 0, missingTable("investment_instruments", err)
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var oldID, asset, market, symbol, name, currency, contract string
		var priceScale, quantityScale int16
		var active bool
		if err := rows.Scan(&oldID, &asset, &market, &symbol, &name, &currency, &contract, &priceScale, &quantityScale, &active); err != nil {
			return count, err
		}
		count++
		if dry {
			continue
		}
		item := &hengcai.InvestmentInstrument{Uid: c.Int64("uid"), AssetType: asset, Market: market, Symbol: symbol, Name: name, Currency: strings.ToUpper(currency), ContractKey: contract, PriceScale: priceScale, QuantityScale: quantityScale, Active: active}
		if _, err := targetSession(c, item.Uid).Insert(item); err != nil {
			return count, err
		}
		m.Instruments[oldID] = item.Id
	}
	return count, rows.Err()
}

func migrateInvestmentTransactions(ctx context.Context, db *sql.DB, c *core.CliContext, m migrationMaps, dry bool) (int, error) {
	rows, err := db.QueryContext(ctx, `SELECT account_id::text, instrument_id::text, EXTRACT(EPOCH FROM traded_at)::bigint, action, quantity::float8, quantity_delta::float8, COALESCE(price,0)::float8, gross_amount_minor, fees_minor, taxes_minor, net_cash_amount_minor, currency, source, COALESCE(note,'') FROM investment_transactions WHERE user_id = $1 AND deleted_at IS NULL ORDER BY traded_at`, c.String("source-user-id"))
	if err != nil {
		return 0, missingTable("investment_transactions", err)
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var accountID, instrumentID, action, currency, source, note string
		var traded int64
		var quantity, delta, price float64
		var gross, fees, taxes, net int64
		if err := rows.Scan(&accountID, &instrumentID, &traded, &action, &quantity, &delta, &price, &gross, &fees, &taxes, &net, &currency, &source, &note); err != nil {
			return count, err
		}
		count++
		if dry {
			continue
		}
		mappedAccount, okA := m.InvestmentAccounts[accountID]
		mappedInstrument, okI := m.Instruments[instrumentID]
		if !okA || !okI {
			continue
		}
		item := &hengcai.InvestmentTransaction{Uid: c.Int64("uid"), InvestmentAccountId: mappedAccount, InstrumentId: mappedInstrument, TradedAt: traded, Action: action, Quantity: quantity, QuantityDelta: delta, Price: price, GrossAmountMinor: gross, FeesMinor: fees, TaxesMinor: taxes, NetCashAmountMinor: net, Currency: strings.ToUpper(currency), Source: source, Note: note}
		if _, err := targetSession(c, item.Uid).Insert(item); err != nil {
			return count, err
		}
	}
	return count, rows.Err()
}

func migrateCapex(ctx context.Context, db *sql.DB, c *core.CliContext, m migrationMaps, dry bool) (int, error) {
	rows, err := db.QueryContext(ctx, `SELECT id::text, purchase_date::text, COALESCE(merchant_name,''), item_name, total_amount_minor, down_payment_minor, installment_count, first_due_date::text, financing_type, interest_fee_total_minor, currency, status, COALESCE(note,'') FROM capex_purchases WHERE user_id = $1 AND deleted_at IS NULL ORDER BY purchase_date`, c.String("source-user-id"))
	if err != nil {
		return 0, missingTable("capex_purchases", err)
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var oldID, date, merchant, itemName, due, financing, currency, status, note string
		var total, down, interest int64
		var installments int
		if err := rows.Scan(&oldID, &date, &merchant, &itemName, &total, &down, &installments, &due, &financing, &interest, &currency, &status, &note); err != nil {
			return count, err
		}
		count++
		if dry {
			continue
		}
		item := &hengcai.CapexPurchase{Uid: c.Int64("uid"), PurchaseDate: date, MerchantName: merchant, ItemName: itemName, TotalAmountMinor: total, DownPaymentMinor: down, InstallmentCount: installments, FirstDueDate: due, FinancingType: financing, InterestFeeTotalMinor: interest, Currency: strings.ToUpper(currency), Status: status, Note: note}
		if _, err := targetSession(c, item.Uid).Insert(item); err != nil {
			return count, err
		}
		m.Purchases[oldID] = item.Id
	}
	return count, rows.Err()
}

func migrateCapexInstallments(ctx context.Context, db *sql.DB, c *core.CliContext, m migrationMaps, dry bool) (int, error) {
	rows, err := db.QueryContext(ctx, `SELECT purchase_id::text, installment_number, due_date::text, principal_minor, interest_minor, fee_minor, COALESCE(actual_paid_minor,0), status FROM capex_installments WHERE deleted_at IS NULL ORDER BY due_date`)
	if err != nil {
		return 0, missingTable("capex_installments", err)
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var purchaseID, due, status string
		var no int
		var principal, interest, fee, paid int64
		if err := rows.Scan(&purchaseID, &no, &due, &principal, &interest, &fee, &paid, &status); err != nil {
			return count, err
		}
		count++
		if dry {
			continue
		}
		mapped, ok := m.Purchases[purchaseID]
		if !ok {
			continue
		}
		item := &hengcai.CapexInstallment{Uid: c.Int64("uid"), PurchaseId: mapped, InstallmentNo: no, DueDate: due, PrincipalMinor: principal, InterestMinor: interest, FeeMinor: fee, ActualPaidMinor: paid, Status: status}
		if _, err := targetSession(c, item.Uid).Insert(item); err != nil {
			return count, err
		}
	}
	return count, rows.Err()
}

func migrateProjections(ctx context.Context, db *sql.DB, c *core.CliContext, dry bool) (int, error) {
	rows, err := db.QueryContext(ctx, `SELECT p.month::text, p.data_type, COALESCE(p.salary_amount_minor,0), COALESCE(p.actual_opex_minor,0), p.capex_minor, p.confirmed_investment_return_minor, COALESCE(p.fcf_minor,0), COALESCE(p.ending_investable_assets_minor,0), p.data_quality_status, p.explanation::text, EXTRACT(EPOCH FROM p.created_at)::bigint FROM monthly_projections p JOIN calculation_runs r ON r.id = p.calculation_run_id WHERE r.user_id = $1 ORDER BY p.month`, c.String("source-user-id"))
	if err != nil {
		return 0, missingTable("monthly_projections", err)
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var month, typ, quality, explanation string
		var income, opex, capex, returns, fcf, assets, created int64
		if err := rows.Scan(&month, &typ, &income, &opex, &capex, &returns, &fcf, &assets, &quality, &explanation, &created); err != nil {
			return count, err
		}
		count++
		if dry {
			continue
		}
		item := &hengcai.CashflowProjection{Uid: c.Int64("uid"), Month: month[:7], DataType: typ, IncomeMinor: income, OpexMinor: opex, CapexMinor: capex, InvestmentReturnMinor: returns, FreeCashflowMinor: fcf, EndingInvestableAssetsMinor: assets, Quality: quality, Explanation: explanation, CreatedUnixTime: created}
		if _, err := targetSession(c, item.Uid).Insert(item); err != nil {
			return count, err
		}
	}
	return count, rows.Err()
}

func migrateStatements(ctx context.Context, db *sql.DB, c *core.CliContext, m migrationMaps, dry bool) (int, error) {
	rows, err := db.QueryContext(ctx, `SELECT id::text, statement_period_start::text, statement_period_end::text, statement_date::text, opening_balance_minor, closing_balance_minor, total_debit_minor, total_credit_minor, currency, COALESCE(artifact_hash,''), balance_valid, reconciliation_status FROM account_statements WHERE user_id = $1 ORDER BY statement_date`, c.String("source-user-id"))
	if err != nil {
		return 0, missingTable("account_statements", err)
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var oldID, start, end, date, currency, hash, recon string
		var opening, closing, debit, credit int64
		var valid bool
		if err := rows.Scan(&oldID, &start, &end, &date, &opening, &closing, &debit, &credit, &currency, &hash, &valid, &recon); err != nil {
			return count, err
		}
		count++
		if dry {
			continue
		}
		item := &hengcai.StatementImport{Uid: c.Int64("uid"), Provider: "MIGRATED", AccountId: c.Int64("account-id"), StatementDate: date, PeriodStart: start, PeriodEnd: end, Currency: currency, OpeningBalanceMinor: opening, ClosingBalanceMinor: closing, TotalDebitMinor: debit, TotalCreditMinor: credit, ArtifactHash: hash, Status: recon, BalanceValid: valid, SummaryValid: valid, ValidationErrors: "[]", RawPayload: "{}", CreatedUnixTime: time.Now().Unix()}
		if _, err := targetSession(c, item.Uid).Insert(item); err != nil {
			return count, err
		}
		m.Statements[oldID] = item.Id
	}
	return count, rows.Err()
}

func migrateStatementLines(ctx context.Context, db *sql.DB, c *core.CliContext, m migrationMaps, dry bool) (int, error) {
	rows, err := db.QueryContext(ctx, `SELECT statement_id::text, line_number, COALESCE(transaction_date::text,''), posted_date::text, description, amount_minor, direction, currency, line_hash, status, raw_payload::text FROM statement_lines WHERE statement_id IN (SELECT id FROM account_statements WHERE user_id = $1) ORDER BY statement_id, line_number`, c.String("source-user-id"))
	if err != nil {
		return 0, missingTable("statement_lines", err)
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var statementID, txDate, posted, description, direction, currency, hash, status, raw string
		var number int
		var amount int64
		if err := rows.Scan(&statementID, &number, &txDate, &posted, &description, &amount, &direction, &currency, &hash, &status, &raw); err != nil {
			return count, err
		}
		count++
		if dry {
			continue
		}
		mapped, ok := m.Statements[statementID]
		if !ok {
			continue
		}
		item := &hengcai.StatementLine{Uid: c.Int64("uid"), StatementId: mapped, LineNumber: number, TransactionDate: txDate, PostedDate: posted, Description: description, AmountMinor: amount, SignedAmountMinor: amount, Direction: direction, Currency: strings.ToUpper(currency), LineHash: hash, Status: status, RawPayload: raw}
		if _, err := targetSession(c, item.Uid).Insert(item); err != nil {
			return count, err
		}
	}
	return count, rows.Err()
}

func missingTable(name string, err error) error {
	if pqErr, ok := err.(*pq.Error); ok && pqErr.Code == "42P01" {
		return fmt.Errorf("source table %s is missing; run with a database containing the recovered schema", name)
	}
	return fmt.Errorf("read %s: %w", name, err)
}
