package hengcai

// The extension tables deliberately use ezBookkeeping's integer uid model.  This
// keeps the feature set portable across SQLite, MySQL and PostgreSQL while the
// original recovery schema remains available as a migration source.

type StatementImport struct {
	Id                  int64  `xorm:"PK AUTOINCR"`
	Uid                 int64  `xorm:"INDEX NOT NULL"`
	Provider            string `xorm:"VARCHAR(32) NOT NULL"`
	AccountId           int64  `xorm:"INDEX NOT NULL"`
	StatementDate       string `xorm:"VARCHAR(10) NOT NULL"`
	PeriodStart         string `xorm:"VARCHAR(10) NOT NULL"`
	PeriodEnd           string `xorm:"VARCHAR(10) NOT NULL"`
	Currency            string `xorm:"VARCHAR(3) NOT NULL"`
	OpeningBalanceMinor int64  `xorm:"NOT NULL"`
	ClosingBalanceMinor int64  `xorm:"NOT NULL"`
	TotalDebitMinor     int64  `xorm:"NOT NULL"`
	TotalCreditMinor    int64  `xorm:"NOT NULL"`
	ArtifactHash        string `xorm:"VARCHAR(64) INDEX NOT NULL"`
	Status              string `xorm:"VARCHAR(24) INDEX NOT NULL"`
	BalanceValid        bool   `xorm:"NOT NULL"`
	SummaryValid        bool   `xorm:"NOT NULL"`
	ValidationErrors    string `xorm:"TEXT NOT NULL"`
	RawPayload          string `xorm:"TEXT NOT NULL"`
	CreatedUnixTime     int64  `xorm:"INDEX NOT NULL"`
	FileName            string `xorm:"VARCHAR(255)" json:"file_name"`
	DisplayName         string `xorm:"VARCHAR(120)" json:"display_name"`
	PeriodType          string `xorm:"VARCHAR(16) INDEX" json:"period_type"`        // CALENDAR_MONTH or BILLING_CYCLE
	CoverageDimension   string `xorm:"VARCHAR(24) INDEX" json:"coverage_dimension"` // MERCHANT_CHANNEL or FUNDING_SOURCE
	CoverageStatus      string `xorm:"VARCHAR(16) INDEX" json:"coverage_status"`
	BillingDate         string `xorm:"VARCHAR(10)" json:"billing_date"`
	DueDate             string `xorm:"VARCHAR(10)" json:"due_date"`
	Revision            int    `xorm:"NOT NULL DEFAULT 1" json:"revision"`
	CoveredUntil        string `xorm:"VARCHAR(10)" json:"covered_until"`
}

func (StatementImport) TableName() string { return "hengcai_statement_import" }

type StatementLine struct {
	Id                      int64  `xorm:"PK AUTOINCR"`
	Uid                     int64  `xorm:"INDEX NOT NULL"`
	StatementId             int64  `xorm:"INDEX NOT NULL"`
	LineNumber              int    `xorm:"NOT NULL"`
	TransactionDate         string `xorm:"VARCHAR(10)"`
	PostedDate              string `xorm:"VARCHAR(10) NOT NULL"`
	Description             string `xorm:"VARCHAR(255) NOT NULL"`
	Counterparty            string `xorm:"VARCHAR(255)" json:"counterparty"`
	CounterpartyType        string `xorm:"VARCHAR(16) INDEX" json:"counterparty_type"`
	ReviewReason            string `xorm:"VARCHAR(255)" json:"review_reason"`
	AmountMinor             int64  `xorm:"NOT NULL"`
	SignedAmountMinor       int64  `xorm:"NOT NULL"`
	Direction               string `xorm:"VARCHAR(8) NOT NULL"`
	Currency                string `xorm:"VARCHAR(3) NOT NULL"`
	CardLastFour            string `xorm:"VARCHAR(4)"`
	Section                 string `xorm:"VARCHAR(32)"`
	LineKind                string `xorm:"VARCHAR(32) INDEX NOT NULL"`
	AccountingTreatment     string `xorm:"VARCHAR(32)"`
	SettlesPriorStatement   bool   `xorm:"NOT NULL"`
	RequiresReview          bool   `xorm:"NOT NULL"`
	LineHash                string `xorm:"VARCHAR(64) INDEX NOT NULL"`
	Classification          string `xorm:"VARCHAR(255)"`
	CategoryId              int64  `xorm:"INDEX" json:"category_id,string"`
	ConfidenceBps           int    `xorm:"NOT NULL"`
	Status                  string `xorm:"VARCHAR(24) INDEX NOT NULL"`
	RawPayload              string `xorm:"TEXT NOT NULL"`
	ExternalReference       string `xorm:"VARCHAR(128) INDEX" json:"external_reference"`
	PaymentChannel          string `xorm:"VARCHAR(32) INDEX" json:"payment_channel"`
	MatchedTransactionId    int64  `xorm:"INDEX" json:"matched_transaction_id,string"`
	MatchType               string `xorm:"VARCHAR(32) INDEX" json:"match_type"`
	MatchScoreBps           int    `xorm:"NOT NULL DEFAULT 0" json:"match_score_bps"`
	MerchantChannel         string `xorm:"VARCHAR(24) INDEX" json:"merchant_channel"`
	FundingSource           string `xorm:"VARCHAR(24) INDEX" json:"funding_source"`
	EntrySource             string `xorm:"VARCHAR(32) INDEX" json:"entry_source"`
	CoverageState           string `xorm:"VARCHAR(16) INDEX" json:"coverage_state"`
	SupersededTransactionId int64  `xorm:"INDEX" json:"superseded_transaction_id,string"`
	StatementCategory       string `xorm:"VARCHAR(64) INDEX" json:"statement_category"`
}

func (StatementLine) TableName() string { return "hengcai_statement_line" }

// StatementPostingReversal preserves an audit trail when a posted statement is
// reopened for corrections before month close. Posting metadata can then be
// rebuilt safely without losing who reversed which statement and why.
type StatementPostingReversal struct {
	Id                      int64  `xorm:"PK AUTOINCR" json:"id"`
	Uid                     int64  `xorm:"INDEX NOT NULL" json:"-"`
	StatementId             int64  `xorm:"INDEX NOT NULL" json:"statement_id,string"`
	Month                   string `xorm:"VARCHAR(7) INDEX NOT NULL" json:"month"`
	Reason                  string `xorm:"VARCHAR(255) NOT NULL" json:"reason"`
	DeletedTransactionCount int    `xorm:"NOT NULL" json:"deleted_transaction_count"`
	RestoredEvidenceCount   int    `xorm:"NOT NULL" json:"restored_evidence_count"`
	RevertedCapexCount      int    `xorm:"NOT NULL" json:"reverted_capex_count"`
	CreatedUnixTime         int64  `xorm:"INDEX NOT NULL" json:"created_unix_time"`
}

func (StatementPostingReversal) TableName() string {
	return "hengcai_statement_posting_reversal"
}

type MonthClose struct {
	Id                        int64  `xorm:"PK AUTOINCR"`
	Uid                       int64  `xorm:"UNIQUE(UQE_hengcai_month_close_identity) NOT NULL"`
	Month                     string `xorm:"UNIQUE(UQE_hengcai_month_close_identity) VARCHAR(7) NOT NULL"`
	Status                    string `xorm:"VARCHAR(16) INDEX NOT NULL"`
	StatementCount            int    `xorm:"NOT NULL"`
	UnmatchedLineCount        int    `xorm:"NOT NULL"`
	ConfirmedTransactionCount int    `xorm:"NOT NULL"`
	ClosedUnixTime            int64  `xorm:"NOT NULL"`
	Note                      string `xorm:"VARCHAR(255)"`
}

func (MonthClose) TableName() string { return "hengcai_month_close" }

// ReconciliationMatch is an auditable link between a normalized statement
// line and either a core transaction or another statement line. It is kept
// outside the core transaction table so upstream ezBookkeeping upgrades remain
// possible and a manual transaction can retain its original identity.
type ReconciliationMatch struct {
	Id                     int64  `xorm:"PK AUTOINCR" json:"id"`
	Uid                    int64  `xorm:"INDEX NOT NULL" json:"-"`
	Month                  string `xorm:"VARCHAR(7) INDEX NOT NULL" json:"month"`
	StatementLineId        int64  `xorm:"INDEX NOT NULL" json:"statement_line_id,string"`
	RelatedStatementLineId int64  `xorm:"INDEX" json:"related_statement_line_id,string"`
	TransactionId          int64  `xorm:"INDEX" json:"transaction_id,string"`
	MatchType              string `xorm:"VARCHAR(32) INDEX NOT NULL" json:"match_type"`
	ScoreBps               int    `xorm:"NOT NULL" json:"score_bps"`
	Status                 string `xorm:"VARCHAR(16) INDEX NOT NULL" json:"status"`
	Reason                 string `xorm:"VARCHAR(255)" json:"reason"`
	CreatedUnixTime        int64  `xorm:"NOT NULL" json:"created_unix_time"`
}

func (ReconciliationMatch) TableName() string { return "hengcai_reconciliation_match" }

// TransactionOrigin records which statement line confirmed or created a core
// transaction. This makes posting idempotent and preserves the source of truth.
type TransactionOrigin struct {
	Id                int64  `xorm:"PK AUTOINCR" json:"id"`
	Uid               int64  `xorm:"UNIQUE(UQE_hengcai_transaction_origin_identity) NOT NULL" json:"-"`
	TransactionId     int64  `xorm:"UNIQUE(UQE_hengcai_transaction_origin_identity) NOT NULL" json:"transaction_id,string"`
	StatementLineId   int64  `xorm:"INDEX NOT NULL" json:"statement_line_id,string"`
	Provider          string `xorm:"VARCHAR(32) INDEX NOT NULL" json:"provider"`
	CategorySource    string `xorm:"VARCHAR(16) NOT NULL" json:"category_source"`
	VerificationState string `xorm:"VARCHAR(16) INDEX NOT NULL" json:"verification_state"`
	CreatedUnixTime   int64  `xorm:"NOT NULL" json:"created_unix_time"`
}

func (TransactionOrigin) TableName() string { return "hengcai_transaction_origin" }

// TransactionEvidence allows one economic transaction to be verified by both
// a merchant statement (Alipay/WeChat) and a settlement statement (bank/card).
// The evidence rows are intentionally separate from the core transaction.
type TransactionEvidence struct {
	Id                int64  `xorm:"PK AUTOINCR" json:"id"`
	Uid               int64  `xorm:"INDEX NOT NULL" json:"-"`
	TransactionId     int64  `xorm:"INDEX NOT NULL" json:"transaction_id,string"`
	StatementLineId   int64  `xorm:"UNIQUE NOT NULL" json:"statement_line_id,string"`
	EvidenceType      string `xorm:"VARCHAR(24) INDEX NOT NULL" json:"evidence_type"`
	MerchantChannel   string `xorm:"VARCHAR(24)" json:"merchant_channel"`
	FundingSource     string `xorm:"VARCHAR(24)" json:"funding_source"`
	VerificationState string `xorm:"VARCHAR(16) INDEX NOT NULL" json:"verification_state"`
	MatchScoreBps     int    `xorm:"NOT NULL" json:"match_score_bps"`
	CreatedUnixTime   int64  `xorm:"NOT NULL" json:"created_unix_time"`
}

func (TransactionEvidence) TableName() string { return "hengcai_transaction_evidence" }

// TransactionCoverage is the rolling watermark for one source dimension and
// account. A calendar-month import and a non-calendar card cycle can therefore
// advance independently without overwriting each other's verification state.
type TransactionCoverage struct {
	Id              int64  `xorm:"PK AUTOINCR" json:"id"`
	Uid             int64  `xorm:"UNIQUE(UQE_hengcai_coverage_identity) NOT NULL" json:"-"`
	Dimension       string `xorm:"UNIQUE(UQE_hengcai_coverage_identity) VARCHAR(24) NOT NULL" json:"dimension"`
	Source          string `xorm:"UNIQUE(UQE_hengcai_coverage_identity) VARCHAR(24) NOT NULL" json:"source"`
	AccountId       int64  `xorm:"UNIQUE(UQE_hengcai_coverage_identity) NOT NULL" json:"account_id,string"`
	PeriodType      string `xorm:"VARCHAR(16) NOT NULL" json:"period_type"`
	CoverageStart   string `xorm:"VARCHAR(10) NOT NULL" json:"coverage_start"`
	CoverageEnd     string `xorm:"VARCHAR(10) NOT NULL" json:"coverage_end"`
	CoveredUntil    string `xorm:"VARCHAR(10) NOT NULL" json:"covered_until"`
	StatementId     int64  `xorm:"INDEX NOT NULL" json:"statement_id,string"`
	Revision        int    `xorm:"NOT NULL" json:"revision"`
	Status          string `xorm:"VARCHAR(16) INDEX NOT NULL" json:"status"`
	UpdatedUnixTime int64  `xorm:"NOT NULL" json:"updated_unix_time"`
}

func (TransactionCoverage) TableName() string { return "hengcai_transaction_coverage" }

// ManualTransactionMarker stores the two provisional ticks selected while a
// user records a core transaction. Statement imports verify these dimensions
// independently and never need to alter ezBookkeeping's upstream schema.
type ManualTransactionMarker struct {
	Id              int64  `xorm:"PK AUTOINCR" json:"id"`
	Uid             int64  `xorm:"UNIQUE(UQE_hengcai_manual_marker_identity) NOT NULL" json:"-"`
	TransactionId   int64  `xorm:"UNIQUE(UQE_hengcai_manual_marker_identity) NOT NULL" json:"transaction_id,string"`
	MerchantChannel string `xorm:"VARCHAR(24) INDEX NOT NULL" json:"merchant_channel"`
	FundingSource   string `xorm:"VARCHAR(24) INDEX NOT NULL" json:"funding_source"`
	MerchantState   string `xorm:"VARCHAR(16) INDEX NOT NULL" json:"merchant_state"`
	FundingState    string `xorm:"VARCHAR(16) INDEX NOT NULL" json:"funding_state"`
	UpdatedUnixTime int64  `xorm:"NOT NULL" json:"updated_unix_time"`
}

func (ManualTransactionMarker) TableName() string { return "hengcai_manual_transaction_marker" }

// AISetting is user scoped. ApiKey is always encrypted with the server secret.
type AISetting struct {
	Id              int64  `xorm:"PK AUTOINCR" json:"id"`
	Uid             int64  `xorm:"UNIQUE NOT NULL" json:"uid"`
	Enabled         bool   `xorm:"NOT NULL" json:"enabled"`
	Provider        string `xorm:"VARCHAR(32) NOT NULL" json:"provider"`
	BaseUrl         string `xorm:"VARCHAR(255) NOT NULL" json:"base_url"`
	ApiKey          string `xorm:"VARCHAR(512) NOT NULL" json:"api_key"`
	Model           string `xorm:"VARCHAR(96) NOT NULL" json:"model"`
	UpdatedUnixTime int64  `xorm:"NOT NULL" json:"updated_unix_time"`
}

func (AISetting) TableName() string { return "hengcai_ai_setting" }

type InvestmentAccount struct {
	Id              int64  `xorm:"PK AUTOINCR" json:"id"`
	Uid             int64  `xorm:"INDEX NOT NULL" json:"uid"`
	Name            string `xorm:"VARCHAR(80) NOT NULL" json:"name"`
	Institution     string `xorm:"VARCHAR(80)" json:"institution"`
	AccountType     string `xorm:"VARCHAR(24) NOT NULL" json:"account_type"`
	BaseCurrency    string `xorm:"VARCHAR(3) NOT NULL" json:"base_currency"`
	AccountId       int64  `xorm:"INDEX" json:"account_id,string"`
	Active          bool   `xorm:"NOT NULL" json:"active"`
	CreatedUnixTime int64  `xorm:"NOT NULL" json:"created_unix_time"`
}

func (InvestmentAccount) TableName() string { return "hengcai_investment_account" }

type InvestmentInstrument struct {
	Id                 int64   `xorm:"PK AUTOINCR" json:"id"`
	Uid                int64   `xorm:"INDEX NOT NULL" json:"uid"`
	AssetType          string  `xorm:"VARCHAR(24) NOT NULL" json:"asset_type"`
	Market             string  `xorm:"VARCHAR(24) NOT NULL" json:"market"`
	Symbol             string  `xorm:"VARCHAR(32) NOT NULL" json:"symbol"`
	Name               string  `xorm:"VARCHAR(120) NOT NULL" json:"name"`
	Currency           string  `xorm:"VARCHAR(3) NOT NULL" json:"currency"`
	ContractKey        string  `xorm:"VARCHAR(80) NOT NULL" json:"contract_key"`
	UnderlyingSymbol   string  `xorm:"VARCHAR(32) NOT NULL" json:"underlying_symbol"`
	ExpirationDate     string  `xorm:"VARCHAR(10) NOT NULL" json:"expiration_date"`
	OptionType         string  `xorm:"VARCHAR(8) NOT NULL" json:"option_type"`
	StrikePrice        float64 `xorm:"NOT NULL" json:"strike_price"`
	ContractMultiplier int     `xorm:"NOT NULL" json:"contract_multiplier"`
	PriceScale         int16   `xorm:"NOT NULL" json:"price_scale"`
	QuantityScale      int16   `xorm:"NOT NULL" json:"quantity_scale"`
	Active             bool    `xorm:"NOT NULL" json:"active"`
}

func (InvestmentInstrument) TableName() string { return "hengcai_investment_instrument" }

type InvestmentTransaction struct {
	Id                  int64   `xorm:"PK AUTOINCR" json:"id"`
	Uid                 int64   `xorm:"INDEX NOT NULL" json:"uid"`
	InvestmentAccountId int64   `xorm:"INDEX NOT NULL" json:"investment_account_id"`
	InstrumentId        int64   `xorm:"INDEX NOT NULL" json:"instrument_id"`
	TradedAt            int64   `xorm:"INDEX NOT NULL" json:"traded_at"`
	Action              string  `xorm:"VARCHAR(24) NOT NULL" json:"action"`
	Quantity            float64 `xorm:"NOT NULL" json:"quantity"`
	QuantityDelta       float64 `xorm:"NOT NULL" json:"quantity_delta"`
	Price               float64 `xorm:"NOT NULL" json:"price"`
	GrossAmountMinor    int64   `xorm:"NOT NULL" json:"gross_amount_minor"`
	FeesMinor           int64   `xorm:"NOT NULL" json:"fees_minor"`
	TaxesMinor          int64   `xorm:"NOT NULL" json:"taxes_minor"`
	NetCashAmountMinor  int64   `xorm:"NOT NULL" json:"net_cash_amount_minor"`
	Currency            string  `xorm:"VARCHAR(3) NOT NULL" json:"currency"`
	Source              string  `xorm:"VARCHAR(24) NOT NULL" json:"source"`
	ExternalReference   string  `xorm:"VARCHAR(120) INDEX" json:"external_reference"`
	Note                string  `xorm:"VARCHAR(255)" json:"note"`
}

func (InvestmentTransaction) TableName() string { return "hengcai_investment_transaction" }

type InvestmentPosition struct {
	Id                  int64   `xorm:"PK AUTOINCR" json:"id"`
	Uid                 int64   `xorm:"INDEX NOT NULL" json:"uid"`
	InvestmentAccountId int64   `xorm:"UNIQUE(UQE_hengcai_position_identity) NOT NULL" json:"investment_account_id"`
	InstrumentId        int64   `xorm:"UNIQUE(UQE_hengcai_position_identity) NOT NULL" json:"instrument_id"`
	Quantity            float64 `xorm:"NOT NULL" json:"quantity"`
	AverageCost         float64 `xorm:"NOT NULL" json:"average_cost"`
	MarketPrice         float64 `xorm:"NOT NULL" json:"market_price"`
	MarketValueMinor    int64   `xorm:"NOT NULL" json:"market_value_minor"`
	CostValueMinor      int64   `xorm:"NOT NULL" json:"cost_value_minor"`
	UnrealizedPnlMinor  int64   `xorm:"NOT NULL" json:"unrealized_pnl_minor"`
	ReturnBps           int     `xorm:"NOT NULL" json:"return_bps"`
	AsOfUnixTime        int64   `xorm:"NOT NULL" json:"as_of_unix_time"`
}

func (InvestmentPosition) TableName() string { return "hengcai_investment_position" }

// InvestmentAccountValuation stores the materialized summary exposed through
// the linked ezBookkeeping investment account. Detailed trades and holdings
// remain in the Hengcai tables; the core account only consumes TotalEquityMinor
// for net-worth presentation.
type InvestmentAccountValuation struct {
	Id                     int64  `xorm:"PK AUTOINCR" json:"id"`
	Uid                    int64  `xorm:"UNIQUE(UQE_hengcai_account_valuation) NOT NULL" json:"uid"`
	InvestmentAccountId    int64  `xorm:"UNIQUE(UQE_hengcai_account_valuation) NOT NULL" json:"investment_account_id"`
	BaseCurrency           string `xorm:"VARCHAR(3) NOT NULL" json:"base_currency"`
	AnchorCashBalanceMinor int64  `xorm:"NOT NULL" json:"anchor_cash_balance_minor"`
	AnchorUnixTime         int64  `xorm:"NOT NULL" json:"anchor_unix_time"`
	PositionValueMinor     int64  `xorm:"NOT NULL" json:"position_value_minor"`
	TotalEquityMinor       int64  `xorm:"NOT NULL" json:"total_equity_minor"`
	Source                 string `xorm:"VARCHAR(32) NOT NULL" json:"source"`
	Quality                string `xorm:"VARCHAR(32) NOT NULL" json:"quality"`
	AsOfUnixTime           int64  `xorm:"NOT NULL" json:"as_of_unix_time"`
	UpdatedUnixTime        int64  `xorm:"NOT NULL" json:"updated_unix_time"`
}

func (InvestmentAccountValuation) TableName() string {
	return "hengcai_investment_account_valuation"
}

type InvestmentReturn struct {
	Id                 int64  `xorm:"PK AUTOINCR" json:"id"`
	Uid                int64  `xorm:"UNIQUE(UQE_hengcai_return_month) NOT NULL" json:"uid"`
	Month              string `xorm:"UNIQUE(UQE_hengcai_return_month) VARCHAR(7) NOT NULL" json:"month"`
	RealizedPnlMinor   int64  `xorm:"NOT NULL" json:"realized_pnl_minor"`
	UnrealizedPnlMinor int64  `xorm:"NOT NULL" json:"unrealized_pnl_minor"`
	TotalReturnMinor   int64  `xorm:"NOT NULL" json:"total_return_minor"`
	ReturnBps          int    `xorm:"NOT NULL" json:"return_bps"`
	Quality            string `xorm:"VARCHAR(24) NOT NULL" json:"quality"`
	UpdatedUnixTime    int64  `xorm:"NOT NULL" json:"updated_unix_time"`
}

func (InvestmentReturn) TableName() string { return "hengcai_investment_return" }

type InvestmentStatementImport struct {
	Id                       int64  `xorm:"PK AUTOINCR" json:"id"`
	Uid                      int64  `xorm:"INDEX NOT NULL" json:"-"`
	InvestmentAccountId      int64  `xorm:"INDEX NOT NULL" json:"investment_account_id"`
	Provider                 string `xorm:"VARCHAR(16) INDEX NOT NULL" json:"provider"`
	BrokerAccount            string `xorm:"VARCHAR(64)" json:"broker_account"`
	PeriodStart              string `xorm:"VARCHAR(10) NOT NULL" json:"period_start"`
	PeriodEnd                string `xorm:"VARCHAR(10) INDEX NOT NULL" json:"period_end"`
	BaseCurrency             string `xorm:"VARCHAR(3) NOT NULL" json:"base_currency"`
	ArtifactHash             string `xorm:"VARCHAR(64) INDEX NOT NULL" json:"artifact_hash"`
	Status                   string `xorm:"VARCHAR(24) INDEX NOT NULL" json:"status"`
	TradeCount               int    `xorm:"NOT NULL" json:"trade_count"`
	HoldingCount             int    `xorm:"NOT NULL" json:"holding_count"`
	ReplacedTransactionCount int    `xorm:"NOT NULL" json:"replaced_transaction_count"`
	OpeningAdjustmentCount   int    `xorm:"NOT NULL" json:"opening_adjustment_count"`
	OpeningNetAssetsMinor    int64  `xorm:"NOT NULL" json:"opening_net_assets_minor"`
	ClosingNetAssetsMinor    int64  `xorm:"NOT NULL" json:"closing_net_assets_minor"`
	PortfolioValueMinor      int64  `xorm:"NOT NULL" json:"portfolio_value_minor"`
	DepositsMinor            int64  `xorm:"NOT NULL" json:"deposits_minor"`
	FeesMinor                int64  `xorm:"NOT NULL" json:"fees_minor"`
	RealizedPnlMinor         int64  `xorm:"NOT NULL" json:"realized_pnl_minor"`
	UnrealizedPnlMinor       int64  `xorm:"NOT NULL" json:"unrealized_pnl_minor"`
	TotalPnlMinor            int64  `xorm:"NOT NULL" json:"total_pnl_minor"`
	ReturnBps                int    `xorm:"NOT NULL" json:"return_bps"`
	TransactionsValid        bool   `xorm:"NOT NULL" json:"transactions_valid"`
	HoldingsValid            bool   `xorm:"NOT NULL" json:"holdings_valid"`
	CashValid                bool   `xorm:"NOT NULL" json:"cash_valid"`
	ValidationErrors         string `xorm:"TEXT NOT NULL" json:"validation_errors"`
	RawPayload               string `xorm:"TEXT NOT NULL" json:"-"`
	CreatedUnixTime          int64  `xorm:"INDEX NOT NULL" json:"created_unix_time"`
}

func (InvestmentStatementImport) TableName() string {
	return "hengcai_investment_statement_import"
}

type MarketPrice struct {
	Id           int64   `xorm:"PK AUTOINCR" json:"id"`
	Uid          int64   `xorm:"INDEX NOT NULL" json:"uid"`
	InstrumentId int64   `xorm:"UNIQUE(UQE_hengcai_market_price_identity) NOT NULL" json:"instrument_id"`
	AsOfUnixTime int64   `xorm:"UNIQUE(UQE_hengcai_market_price_identity) NOT NULL" json:"as_of_unix_time"`
	Provider     string  `xorm:"VARCHAR(24) NOT NULL" json:"provider"`
	Feed         string  `xorm:"VARCHAR(24)" json:"feed"`
	Open         float64 `xorm:"NOT NULL" json:"open"`
	High         float64 `xorm:"NOT NULL" json:"high"`
	Low          float64 `xorm:"NOT NULL" json:"low"`
	Close        float64 `xorm:"NOT NULL" json:"close"`
	Volume       float64 `xorm:"NOT NULL" json:"volume"`
	RawPayload   string  `xorm:"TEXT" json:"-"`
}

func (MarketPrice) TableName() string { return "hengcai_market_price" }

type AlpacaSetting struct {
	Id               int64  `xorm:"PK AUTOINCR" json:"id"`
	Uid              int64  `xorm:"UNIQUE NOT NULL" json:"uid"`
	Environment      string `xorm:"VARCHAR(8) NOT NULL" json:"environment"`
	ApiKeyId         string `xorm:"VARCHAR(128) NOT NULL" json:"api_key_id"`
	SecretKey        string `xorm:"VARCHAR(255) NOT NULL" json:"secret_key"`
	TradingUrl       string `xorm:"VARCHAR(255) NOT NULL" json:"trading_url"`
	DataUrl          string `xorm:"VARCHAR(255) NOT NULL" json:"data_url"`
	LastSyncUnixTime int64  `xorm:"NOT NULL DEFAULT 0" json:"last_sync_unix_time"`
	UpdatedUnixTime  int64  `xorm:"NOT NULL" json:"updated_unix_time"`
}

func (AlpacaSetting) TableName() string { return "hengcai_alpaca_setting" }

type CapexPurchase struct {
	Id                    int64  `xorm:"PK AUTOINCR" json:"id"`
	Uid                   int64  `xorm:"INDEX NOT NULL" json:"uid"`
	PurchaseDate          string `xorm:"VARCHAR(10) NOT NULL" json:"purchase_date"`
	MerchantName          string `xorm:"VARCHAR(120)" json:"merchant_name"`
	ItemName              string `xorm:"VARCHAR(160) NOT NULL" json:"item_name"`
	TotalAmountMinor      int64  `xorm:"NOT NULL" json:"total_amount_minor"`
	DownPaymentMinor      int64  `xorm:"NOT NULL" json:"down_payment_minor"`
	InstallmentCount      int    `xorm:"NOT NULL" json:"installment_count"`
	FirstDueDate          string `xorm:"VARCHAR(10) NOT NULL" json:"first_due_date"`
	FinancingType         string `xorm:"VARCHAR(24) NOT NULL" json:"financing_type"`
	InterestFeeTotalMinor int64  `xorm:"NOT NULL" json:"interest_fee_total_minor"`
	Currency              string `xorm:"VARCHAR(3) NOT NULL" json:"currency"`
	Status                string `xorm:"VARCHAR(16) INDEX NOT NULL" json:"status"`
	Note                  string `xorm:"VARCHAR(255)" json:"note"`
}

func (CapexPurchase) TableName() string { return "hengcai_capex_purchase" }

type CapexInstallment struct {
	Id              int64  `xorm:"PK AUTOINCR" json:"id"`
	Uid             int64  `xorm:"INDEX NOT NULL" json:"uid"`
	PurchaseId      int64  `xorm:"INDEX NOT NULL" json:"purchase_id"`
	InstallmentNo   int    `xorm:"NOT NULL" json:"installment_no"`
	DueDate         string `xorm:"VARCHAR(10) NOT NULL" json:"due_date"`
	PrincipalMinor  int64  `xorm:"NOT NULL" json:"principal_minor"`
	InterestMinor   int64  `xorm:"NOT NULL" json:"interest_minor"`
	FeeMinor        int64  `xorm:"NOT NULL" json:"fee_minor"`
	ActualPaidMinor int64  `xorm:"NOT NULL" json:"actual_paid_minor"`
	Status          string `xorm:"VARCHAR(16) INDEX NOT NULL" json:"status"`
}

func (CapexInstallment) TableName() string { return "hengcai_capex_installment" }

// CapexInstallmentSettlement links a statement principal line to the CAPEX
// installment it settles. Keeping this as a separate audit record supports
// partial/split payments without turning principal into an operating expense.
type CapexInstallmentSettlement struct {
	Id              int64 `xorm:"PK AUTOINCR" json:"id"`
	Uid             int64 `xorm:"INDEX NOT NULL" json:"uid"`
	StatementLineId int64 `xorm:"UNIQUE(UQE_hengcai_capex_settlement_line) NOT NULL" json:"statement_line_id"`
	InstallmentId   int64 `xorm:"INDEX NOT NULL" json:"installment_id"`
	PrincipalMinor  int64 `xorm:"NOT NULL" json:"principal_minor"`
	Posted          bool  `xorm:"NOT NULL DEFAULT 0" json:"posted"`
	CreatedUnixTime int64 `xorm:"NOT NULL" json:"created_unix_time"`
	UpdatedUnixTime int64 `xorm:"NOT NULL" json:"updated_unix_time"`
}

func (CapexInstallmentSettlement) TableName() string {
	return "hengcai_capex_installment_settlement"
}

type BudgetCapacity struct {
	Id              int64  `xorm:"PK AUTOINCR" json:"id"`
	Uid             int64  `xorm:"UNIQUE(UQE_hengcai_budget_capacity_identity) NOT NULL" json:"uid"`
	Month           string `xorm:"UNIQUE(UQE_hengcai_budget_capacity_identity) VARCHAR(7) NOT NULL" json:"month"`
	ReserveMinor    int64  `xorm:"NOT NULL" json:"reserve_minor"`
	CommittedMinor  int64  `xorm:"NOT NULL" json:"committed_minor"`
	AvailableMinor  int64  `xorm:"NOT NULL" json:"available_minor"`
	HorizonMonths   int    `xorm:"NOT NULL" json:"horizon_months"`
	Status          string `xorm:"VARCHAR(16) NOT NULL" json:"status"`
	UpdatedUnixTime int64  `xorm:"NOT NULL" json:"updated_unix_time"`
}

func (BudgetCapacity) TableName() string { return "hengcai_budget_capacity" }

type CashflowProjection struct {
	Id                          int64  `xorm:"PK AUTOINCR" json:"id"`
	Uid                         int64  `xorm:"INDEX NOT NULL" json:"uid"`
	Month                       string `xorm:"INDEX NOT NULL" json:"month"`
	DataType                    string `xorm:"VARCHAR(12) NOT NULL" json:"data_type"`
	IncomeMinor                 int64  `xorm:"NOT NULL" json:"income_minor"`
	OpexMinor                   int64  `xorm:"NOT NULL" json:"opex_minor"`
	CapexMinor                  int64  `xorm:"NOT NULL" json:"capex_minor"`
	InvestmentReturnMinor       int64  `xorm:"NOT NULL" json:"investment_return_minor"`
	FreeCashflowMinor           int64  `xorm:"NOT NULL" json:"free_cashflow_minor"`
	EndingInvestableAssetsMinor int64  `xorm:"NOT NULL" json:"ending_investable_assets_minor"`
	Quality                     string `xorm:"VARCHAR(32) NOT NULL" json:"quality"`
	Explanation                 string `xorm:"TEXT" json:"explanation"`
	CreatedUnixTime             int64  `xorm:"NOT NULL" json:"created_unix_time"`
}

func (CashflowProjection) TableName() string { return "hengcai_cashflow_projection" }

// IncomeForecastSetting is the stable income basis used for future cash-flow
// projections. Actual income remains derived from posted reconciliation lines
// and is deliberately not stored as a manually editable override.
type IncomeForecastSetting struct {
	Id                        int64 `xorm:"PK AUTOINCR" json:"id"`
	Uid                       int64 `xorm:"UNIQUE NOT NULL" json:"-"`
	MonthlySalaryMinor        int64 `xorm:"NOT NULL" json:"monthly_salary_minor"`
	MonthlyPerformanceMinor   int64 `xorm:"NOT NULL DEFAULT 0" json:"monthly_performance_minor"`
	QuarterlyPerformanceMinor int64 `xorm:"NOT NULL DEFAULT 0" json:"quarterly_performance_minor"`
	AnnualPerformanceMinor    int64 `xorm:"NOT NULL" json:"annual_performance_minor"`
	PerformanceMonth          int   `xorm:"NOT NULL" json:"performance_month"`
	UpdatedUnixTime           int64 `xorm:"NOT NULL" json:"updated_unix_time"`
}

func (IncomeForecastSetting) TableName() string { return "hengcai_income_forecast_setting" }
