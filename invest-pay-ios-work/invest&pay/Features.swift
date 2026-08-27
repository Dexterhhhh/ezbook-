import Charts
import SwiftUI
import UniformTypeIdentifiers

struct OverviewView: View {
    @Environment(AppStore.self) private var store

    var body: some View {
        NavigationStack {
            ScrollView {
                VStack(spacing: 16) {
                    ConnectionBanner()
                    MonthSelector()

                    if let dashboard = store.dashboard {
                        StatusHeader(
                            title: dashboard.dataType == "FORECAST" ? "预测月份" : "实际月份",
                            status: dashboard.periodStatus,
                            quality: dashboard.dataQualityStatus
                        )

                        LazyVGrid(
                            columns: [GridItem(.flexible()), GridItem(.flexible())],
                            spacing: 12
                        ) {
                            MetricCard(
                                title: "收入",
                                value: Money.format(dashboard.salaryAmountMinor),
                                icon: "banknote.fill",
                                tint: .green
                            )
                            MetricCard(
                                title: "实际 OPEX",
                                value: Money.format(dashboard.actualOpexMinor),
                                icon: "cart.fill",
                                tint: .orange
                            )
                            MetricCard(
                                title: "标准化 OPEX",
                                value: Money.format(dashboard.normalizedOpexMinor),
                                icon: "waveform.path.ecg",
                                tint: .blue
                            )
                            MetricCard(
                                title: "自由现金流",
                                value: Money.format(dashboard.fcfMinor),
                                icon: "arrow.up.right.circle.fill",
                                tint: .indigo
                            )
                            MetricCard(
                                title: "CAPEX",
                                value: Money.format(dashboard.capexMinor),
                                icon: "building.2.fill",
                                tint: .purple
                            )
                            MetricCard(
                                title: "可投资资产",
                                value: Money.format(dashboard.endingInvestableAssetsMinor),
                                icon: "chart.line.uptrend.xyaxis",
                                tint: .teal
                            )
                        }

                        if dashboard.unclassifiedItemCount > 0 {
                            Label(
                                "\(dashboard.unclassifiedItemCount) 笔流水待分类",
                                systemImage: "exclamationmark.triangle.fill"
                            )
                            .foregroundStyle(.orange)
                            .frame(maxWidth: .infinity, alignment: .leading)
                            .padding()
                            .background(.orange.opacity(0.1), in: .rect(cornerRadius: 14))
                        }
                    } else if store.isLoading {
                        ProgressView("正在加载财务数据…")
                            .frame(maxWidth: .infinity, minHeight: 240)
                    } else {
                        ContentUnavailableView(
                            "暂无概览数据",
                            systemImage: "chart.pie",
                            description: Text("请确认 API 已启动并刷新")
                        )
                    }
                }
                .padding()
            }
            .navigationTitle("衡财")
            .toolbar { RefreshToolbar() }
            .appAlert()
            .refreshable { await store.refreshAll() }
        }
    }
}

struct LedgerView: View {
    @Environment(AppStore.self) private var store
    @State private var showingNewEntry = false
    @State private var segment = LedgerSegment.provisional

    var body: some View {
        NavigationStack {
            VStack(spacing: 0) {
                MonthSelector()
                    .padding(.horizontal)
                    .padding(.bottom, 8)

                Picker("账本类型", selection: $segment) {
                    ForEach(LedgerSegment.allCases) { segment in
                        Text(segment.title).tag(segment)
                    }
                }
                .pickerStyle(.segmented)
                .padding(.horizontal)
                .padding(.bottom, 8)

                if segment == .provisional {
                    provisionalList
                } else {
                    officialList
                }
            }
            .navigationTitle("记账")
            .toolbar {
                ToolbarItem(placement: .primaryAction) {
                    Button("记一笔", systemImage: "plus") {
                        showingNewEntry = true
                    }
                    .disabled(!store.isConnected)
                }
            }
            .sheet(isPresented: $showingNewEntry) {
                NewProvisionalEntryView()
            }
            .appAlert()
            .refreshable { await store.refreshAll() }
        }
    }

    private var provisionalList: some View {
        Group {
            if store.provisionalEntries.isEmpty {
                ContentUnavailableView(
                    "没有临时记录",
                    systemImage: "tray",
                    description: Text("iPhone 手工录入会先进入这里，等待账单校准")
                )
            } else {
                List(store.provisionalEntries) { entry in
                    VStack(alignment: .leading, spacing: 8) {
                        HStack {
                            VStack(alignment: .leading, spacing: 3) {
                                Text(entry.merchantName ?? "未填写商户")
                                    .font(.headline)
                                Text(entry.itemName ?? entry.occurredAt)
                                    .font(.caption)
                                    .foregroundStyle(.secondary)
                            }
                            Spacer()
                            Text(Money.format(abs(entry.amountMinor), currency: entry.currency))
                                .font(.headline.monospacedDigit())
                        }

                        HStack {
                            StatusPill(text: entry.paymentMethod)
                            StatusPill(text: entry.status)
                            if entry.isExceptional == true {
                                StatusPill(text: "一次性异常", tint: .orange)
                            }
                            Spacer()
                            if entry.paymentMethod.uppercased() == "CASH",
                               entry.categoryID != nil,
                               entry.itemName != nil {
                                Button("确认入账") {
                                    Task { await store.confirmCashEntry(entry) }
                                }
                                .buttonStyle(.bordered)
                                .controlSize(.small)
                            }
                        }
                    }
                    .padding(.vertical, 4)
                }
                .listStyle(.plain)
            }
        }
    }

    private var officialList: some View {
        Group {
            if store.expenses.isEmpty {
                ContentUnavailableView(
                    "没有正式账目",
                    systemImage: "book.closed",
                    description: Text("经账单校准或现金确认后会显示在这里")
                )
            } else {
                List(store.expenses) { expense in
                    VStack(alignment: .leading, spacing: 6) {
                        HStack {
                            Text(expense.merchantName ?? "未填写商户")
                                .font(.headline)
                            Spacer()
                            Text(Money.format(expense.totalAmountMinor))
                                .font(.headline.monospacedDigit())
                        }
                        HStack {
                            Text(expense.occurredOn)
                            Spacer()
                            Text(expense.items.map(\.categoryName).uniqued().joined(separator: "、"))
                        }
                        .font(.caption)
                        .foregroundStyle(.secondary)
                    }
                    .padding(.vertical, 4)
                }
                .listStyle(.plain)
            }
        }
    }
}

struct NewProvisionalEntryView: View {
    @Environment(AppStore.self) private var store
    @Environment(\.dismiss) private var dismiss
    @State private var date = Date()
    @State private var amount = ""
    @State private var merchant = ""
    @State private var itemName = ""
    @State private var paymentMethod = "CREDIT_CARD"
    @State private var categoryID = ""
    @State private var isExceptional = false
    @State private var note = ""
    @State private var isSaving = false

    var body: some View {
        NavigationStack {
            Form {
                Section("消费") {
                    DatePicker("日期", selection: $date, displayedComponents: .date)
                    #if os(iOS)
                    TextField("金额", text: $amount)
                        .keyboardType(.decimalPad)
                    #else
                    TextField("金额", text: $amount)
                    #endif
                    TextField("商户", text: $merchant)
                    TextField("物品或用途", text: $itemName)
                }

                Section("分类") {
                    Picker("支付方式", selection: $paymentMethod) {
                        Text("信用卡").tag("CREDIT_CARD")
                        Text("银行卡").tag("DEBIT_CARD")
                        Text("现金").tag("CASH")
                    }
                    Picker("支出方向", selection: $categoryID) {
                        Text("请选择").tag("")
                        ForEach(store.categories) { category in
                            Text(category.name).tag(category.id)
                        }
                    }
                    Toggle("一次性异常消费", isOn: $isExceptional)
                    TextField("备注（可选）", text: $note, axis: .vertical)
                }

                Section {
                    Text("银行卡和信用卡记录不会直接进入正式账本；现金记录可在保存后人工确认。")
                        .font(.footnote)
                        .foregroundStyle(.secondary)
                }
            }
            .navigationTitle("记一笔")
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("取消") { dismiss() }
                }
                ToolbarItem(placement: .confirmationAction) {
                    Button(isSaving ? "保存中…" : "保存") {
                        save()
                    }
                    .disabled(!isValid || isSaving)
                }
            }
        }
    }

    private var isValid: Bool {
        guard let minor = Money.parseMinor(amount), minor > 0 else { return false }
        return !merchant.trimmingCharacters(in: .whitespaces).isEmpty
            && !itemName.trimmingCharacters(in: .whitespaces).isEmpty
            && !categoryID.isEmpty
    }

    private func save() {
        guard let minor = Money.parseMinor(amount), minor > 0 else { return }
        isSaving = true
        let draft = ProvisionalDraft(
            occurredAt: date,
            amountMinor: minor,
            merchantName: merchant.trimmingCharacters(in: .whitespaces),
            itemName: itemName.trimmingCharacters(in: .whitespaces),
            paymentMethod: paymentMethod,
            categoryID: categoryID,
            isExceptional: isExceptional,
            note: note.isEmpty ? nil : note
        )
        Task {
            if await store.addProvisional(draft) {
                dismiss()
            }
            isSaving = false
        }
    }
}

struct StatementsView: View {
    @Environment(AppStore.self) private var store
    @State private var sort = StatementSort.date
    @State private var confirmingPost = false
    @State private var confirmingClose = false
    @State private var showingFileImporter = false

    var body: some View {
        NavigationStack {
            List {
                Section {
                    MonthSelector()
                    if let reconciliation = store.reconciliation {
                        LabeledContent("所需账户", value: "\(reconciliation.receivedStatementCount)/\(reconciliation.requiredAccountCount)")
                        LabeledContent("未匹配账单流水", value: "\(reconciliation.unmatchedLineCount)")
                        LabeledContent("未处理临时记录", value: "\(reconciliation.unmatchedProvisionalCount)")
                        LabeledContent("月份状态", value: reconciliation.status)
                    }
                }

                Section("已导入账单") {
                    if store.statements.isEmpty {
                        Text("当前月份没有账单")
                            .foregroundStyle(.secondary)
                    }
                    ForEach(store.statements) { statement in
                        VStack(alignment: .leading, spacing: 6) {
                            HStack {
                                Text("\(statement.statementPeriodStart) 至 \(statement.statementPeriodEnd)")
                                    .font(.headline)
                                Spacer()
                                Image(systemName: statement.balanceValid ? "checkmark.seal.fill" : "exclamationmark.triangle.fill")
                                    .foregroundStyle(statement.balanceValid ? .green : .orange)
                            }
                            HStack {
                                Text("\(statement.lineCount) 笔")
                                Spacer()
                                Text("期末 \(Money.format(statement.closingBalanceMinor))")
                            }
                            .font(.caption)
                            .foregroundStyle(.secondary)
                        }
                        .padding(.vertical, 3)
                    }
                }

                if let preview = store.postingPreview {
                    Section {
                        Picker("排序", selection: $sort) {
                            ForEach(StatementSort.allCases) { value in
                                Text(value.title).tag(value)
                            }
                        }
                        .pickerStyle(.segmented)
                    } header: {
                        Text("自然月审核")
                    } footer: {
                        Text("金额排序按绝对值，与 Web 端规则一致。")
                    }

                    Section("待推送流水") {
                        if sortedLines.isEmpty {
                            Text("没有待推送流水")
                                .foregroundStyle(.secondary)
                        }
                        ForEach(sortedLines) { line in
                            VStack(alignment: .leading, spacing: 5) {
                                HStack {
                                    Text(line.description)
                                        .font(.headline)
                                    Spacer()
                                    Text(Money.format(line.amountMinor))
                                        .font(.subheadline.monospacedDigit())
                                }
                                HStack {
                                    Text(line.effectiveDate)
                                    Text(line.categoryName ?? line.exclusionReason ?? "待分类")
                                    Spacer()
                                    if line.isExceptional {
                                        StatusPill(text: "异常", tint: .orange)
                                    }
                                }
                                .font(.caption)
                                .foregroundStyle(.secondary)
                            }
                            .padding(.vertical, 2)
                        }
                    }

                    Section {
                        Button("推送 \(preview.eligibleExpenseCount) 笔至正式账本") {
                            confirmingPost = true
                        }
                        .disabled(!preview.canPost)

                        Button("完成 \(store.selectedMonth) 月结") {
                            confirmingClose = true
                        }
                        .disabled(store.reconciliation?.canClose != true)
                    } footer: {
                        if preview.pendingClassificationCount > 0 {
                            Text("仍有 \(preview.pendingClassificationCount) 笔流水待分类，暂不能推送。")
                        }
                    }
                }
            }
            .navigationTitle("账单")
            .toolbar {
                ToolbarItem(placement: .primaryAction) {
                    Button("导入 PDF", systemImage: "square.and.arrow.down") {
                        showingFileImporter = true
                    }
                    .disabled(!store.isConnected)
                }
                RefreshToolbar()
            }
            .task { await store.refreshStatementReview(showError: false) }
            .fileImporter(
                isPresented: $showingFileImporter,
                allowedContentTypes: [.pdf],
                allowsMultipleSelection: false
            ) { result in
                switch result {
                case .success(let urls):
                    guard let url = urls.first else { return }
                    let accessed = url.startAccessingSecurityScopedResource()
                    defer {
                        if accessed { url.stopAccessingSecurityScopedResource() }
                    }
                    do {
                        let data = try Data(contentsOf: url)
                        Task { await store.previewStatement(data: data) }
                    } catch {
                        store.alert = AppAlert(
                            title: "无法读取 PDF",
                            message: error.localizedDescription
                        )
                    }
                case .failure(let error):
                    store.alert = AppAlert(
                        title: "文件选择失败",
                        message: error.localizedDescription
                    )
                }
            }
            .sheet(
                isPresented: Binding(
                    get: { store.importedStatementPreview != nil },
                    set: { if !$0 { store.cancelStatementImport() } }
                )
            ) {
                StatementImportReviewView()
            }
            .confirmationDialog(
                "账单将覆盖同月同支付账户类型的手工临时记录，但保留审计链。",
                isPresented: $confirmingPost,
                titleVisibility: .visible
            ) {
                Button("确认推送") {
                    Task { await store.postStatementLines() }
                }
            }
            .confirmationDialog(
                "月结后该月将锁定，并进入可信预测历史。",
                isPresented: $confirmingClose,
                titleVisibility: .visible
            ) {
                Button("确认月结") {
                    Task { await store.closeMonth() }
                }
            }
            .appAlert()
            .refreshable { await store.refreshAll() }
        }
    }

    private var sortedLines: [StatementPostingLine] {
        guard let lines = store.postingPreview?.lines else { return [] }
        switch sort {
        case .date:
            return lines.sorted { $0.effectiveDate < $1.effectiveDate }
        case .amountAscending:
            return lines.sortedByAbsoluteAmount(ascending: true)
        case .amountDescending:
            return lines.sortedByAbsoluteAmount(ascending: false)
        }
    }
}

struct StatementImportReviewView: View {
    @Environment(AppStore.self) private var store
    @Environment(\.dismiss) private var dismiss
    @State private var accountID = ""
    @State private var isConfirming = false

    var body: some View {
        NavigationStack {
            Form {
                if let preview = store.importedStatementPreview {
                    Section("识别结果") {
                        LabeledContent("提供方", value: preview.provider)
                        LabeledContent("账单日", value: preview.statementDate)
                        LabeledContent(
                            "账期",
                            value: "\(preview.statementPeriodStart) 至 \(preview.statementPeriodEnd)"
                        )
                        LabeledContent("流水", value: "\(preview.lines.count) 笔")
                        LabeledContent("期末余额", value: Money.format(preview.closingBalanceMinor))
                        ValidationRow(title: "余额校验", passed: preview.balanceValid)
                        ValidationRow(title: "摘要校验", passed: preview.summaryValid)
                    }

                    if !preview.validationErrors.isEmpty {
                        Section("校验问题") {
                            ForEach(preview.validationErrors, id: \.self) {
                                Label($0, systemImage: "exclamationmark.triangle.fill")
                                    .foregroundStyle(.orange)
                            }
                        }
                    }

                    Section("入账账户") {
                        Picker("信用卡", selection: $accountID) {
                            Text("请选择").tag("")
                            ForEach(creditCardAccounts) { account in
                                Text(accountLabel(account)).tag(account.id)
                            }
                        }
                    }

                    Section {
                        Text("只有余额校验和摘要校验都通过，且没有识别错误时才能确认。确认后仍需在账单页按自然月审核与分类。")
                            .font(.footnote)
                            .foregroundStyle(.secondary)
                    }
                }
            }
            .navigationTitle("账单预览")
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("取消") {
                        store.cancelStatementImport()
                        dismiss()
                    }
                }
                ToolbarItem(placement: .confirmationAction) {
                    Button(isConfirming ? "导入中…" : "确认导入") {
                        confirm()
                    }
                    .disabled(
                        accountID.isEmpty
                            || store.importedStatementPreview?.canConfirm != true
                            || isConfirming
                    )
                }
            }
            .appAlert()
        }
    }

    private var creditCardAccounts: [FinancialAccount] {
        store.financialAccounts.filter {
            $0.isActive && $0.accountType == "CREDIT_CARD" && $0.currency == "CNY"
        }
    }

    private func accountLabel(_ account: FinancialAccount) -> String {
        let suffix = account.accountLastFour.map { " · \($0)" } ?? ""
        return "\(account.institutionName) \(account.accountName)\(suffix)"
    }

    private func confirm() {
        isConfirming = true
        Task {
            if await store.confirmStatementImport(accountID: accountID) {
                dismiss()
            }
            isConfirming = false
        }
    }
}

struct ValidationRow: View {
    let title: String
    let passed: Bool

    var body: some View {
        LabeledContent(title) {
            Label(
                passed ? "通过" : "未通过",
                systemImage: passed ? "checkmark.circle.fill" : "xmark.circle.fill"
            )
            .foregroundStyle(passed ? .green : .red)
        }
    }
}

struct TrendsView: View {
    @Environment(AppStore.self) private var store
    @State private var metric = TrendMetric.opex

    var body: some View {
        NavigationStack {
            ScrollView {
                VStack(alignment: .leading, spacing: 18) {
                    Picker("指标", selection: $metric) {
                        ForEach(TrendMetric.allCases) { item in
                            Text(item.title).tag(item)
                        }
                    }
                    .pickerStyle(.segmented)

                    if let series = store.trendSeries, !series.points.isEmpty {
                        Chart(series.points) { point in
                            if let value = metric.value(point) {
                                LineMark(
                                    x: .value("月份", point.month),
                                    y: .value(metric.title, Money.decimal(minor: value))
                                )
                                .foregroundStyle(by: .value("类型", point.dataType == "FORECAST" ? "预测" : "实际"))
                                .lineStyle(StrokeStyle(
                                    lineWidth: 3,
                                    dash: point.dataType == "FORECAST" ? [6, 4] : []
                                ))
                                PointMark(
                                    x: .value("月份", point.month),
                                    y: .value(metric.title, Money.decimal(minor: value))
                                )
                                .foregroundStyle(by: .value("类型", point.dataType == "FORECAST" ? "预测" : "实际"))
                            }
                        }
                        .chartYAxis {
                            AxisMarks(format: Decimal.FormatStyle.Currency(code: "CNY").notation(.compactName))
                        }
                        .chartLegend(position: .bottom)
                        .frame(height: 300)
                        .padding()
                        .background(.background.secondary, in: .rect(cornerRadius: 16))

                        HStack {
                            SummaryBadge(title: "实际月份", value: series.actualMonthCount)
                            SummaryBadge(title: "已关闭历史", value: series.closedHistoryMonthCount)
                            SummaryBadge(title: "预测月份", value: series.forecastMonthCount)
                        }
                    } else {
                        ContentUnavailableView(
                            "暂无趋势数据",
                            systemImage: "chart.xyaxis.line",
                            description: Text("关闭更多月份后可形成可信历史")
                        )
                        .frame(maxWidth: .infinity, minHeight: 300)
                    }
                }
                .padding()
            }
            .navigationTitle("趋势")
            .toolbar { RefreshToolbar() }
            .appAlert()
            .refreshable { await store.refreshAll() }
        }
    }
}

struct InvestmentsView: View {
    @Environment(AppStore.self) private var store

    var body: some View {
        NavigationStack {
            List {
                if store.positions.isEmpty {
                    ContentUnavailableView(
                        "暂无持仓",
                        systemImage: "chart.line.uptrend.xyaxis",
                        description: Text("在后端登记投资交易后会显示在这里")
                    )
                }

                ForEach(groupedCurrencies, id: \.self) { currency in
                    Section {
                        ForEach(positions(currency: currency)) { position in
                            VStack(alignment: .leading, spacing: 7) {
                                HStack {
                                    VStack(alignment: .leading, spacing: 2) {
                                        Text(position.symbol)
                                            .font(.headline)
                                        Text(position.name)
                                            .font(.caption)
                                            .foregroundStyle(.secondary)
                                            .lineLimit(1)
                                    }
                                    Spacer()
                                    VStack(alignment: .trailing, spacing: 2) {
                                        Text(Money.format(position.marketValueMinor, currency: currency))
                                            .font(.headline.monospacedDigit())
                                        Text("\(position.quantity) 股 · \(position.positionSide)")
                                            .font(.caption)
                                            .foregroundStyle(.secondary)
                                    }
                                }
                                HStack {
                                    StatusPill(text: position.assetType.uppercased())
                                    if let quality = position.priceQuality {
                                        StatusPill(text: quality, tint: quality == "LIVE" ? .green : .secondary)
                                    }
                                }
                            }
                            .padding(.vertical, 4)
                        }
                    } header: {
                        HStack {
                            Text(currency)
                            Spacer()
                            Text(Money.format(total(currency: currency), currency: currency))
                        }
                    }
                }

                Section("投资账户") {
                    ForEach(store.investmentAccounts) { account in
                        LabeledContent {
                            Text(account.baseCurrency)
                        } label: {
                            VStack(alignment: .leading) {
                                Text(account.name)
                                Text(account.institution ?? account.accountType)
                                    .font(.caption)
                                    .foregroundStyle(.secondary)
                            }
                        }
                    }
                }
            }
            .navigationTitle("投资")
            .toolbar { RefreshToolbar() }
            .appAlert()
            .refreshable { await store.refreshAll() }
        }
    }

    private var groupedCurrencies: [String] {
        Array(Set(store.positions.map(\.currency))).sorted()
    }

    private func positions(currency: String) -> [Position] {
        store.positions.filter { $0.currency == currency }
    }

    private func total(currency: String) -> Int64? {
        let values = positions(currency: currency).compactMap(\.marketValueMinor)
        guard !values.isEmpty else { return nil }
        return values.reduce(0, +)
    }
}

struct SettingsView: View {
    @Environment(AppStore.self) private var store
    @FocusState private var baseURLFocused: Bool
    @State private var apiKey = ProcessInfo.processInfo.environment[
        "HENGCAI_API_KEY_OVERRIDE"
    ] ?? ""
    @State private var password = ProcessInfo.processInfo.environment[
        "HENGCAI_API_PASSWORD_OVERRIDE"
    ] ?? ""
    @State private var deviceName = ProcessInfo.processInfo.environment[
        "HENGCAI_DEVICE_NAME_OVERRIDE"
    ] ?? "iPhone"

    var body: some View {
        @Bindable var store = store
        NavigationStack {
            Form {
                Section {
                    #if os(iOS)
                    TextField("API 地址", text: $store.baseURL)
                        .textInputAutocapitalization(.never)
                        .autocorrectionDisabled()
                        .keyboardType(.URL)
                        .focused($baseURLFocused)
                    #else
                    TextField("API 地址", text: $store.baseURL)
                        .focused($baseURLFocused)
                    #endif
                    Button("保存并测试连接") {
                        baseURLFocused = false
                        Task { await store.saveBaseURL() }
                    }
                } header: {
                    Text("服务器")
                } footer: {
                    Text("模拟器可使用 http://127.0.0.1:8080；真机应使用 HTTPS 或私人 VPN。")
                }

                Section {
                    if store.isAuthenticated {
                        Label("已通过设备令牌认证", systemImage: "checkmark.shield.fill")
                            .foregroundStyle(.green)
                        if let device = store.currentDevice {
                            LabeledContent("设备", value: device.deviceName)
                            LabeledContent("类型", value: device.deviceType)
                        }
                        Button("撤销本设备", role: .destructive) {
                            Task { await store.revokeCurrentDevice() }
                        }
                    } else {
                        TextField("设备名称", text: $deviceName)
                            .textInputAutocapitalization(.never)
                            .autocorrectionDisabled()
                        SecureField("API Key", text: $apiKey)
                            .textInputAutocapitalization(.never)
                            .autocorrectionDisabled()
                        SecureField("密码", text: $password)
                        Button("登录并绑定本设备") {
                            Task {
                                if await store.login(
                                    apiKey: apiKey,
                                    password: password,
                                    deviceName: deviceName
                                ) {
                                    apiKey = ""
                                    password = ""
                                }
                            }
                        }
                        .disabled(
                            apiKey.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty ||
                            password.isEmpty ||
                            deviceName.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
                        )
                    }
                } header: {
                    Text("身份认证")
                } footer: {
                    Text("API Key 和密码只用于换取设备令牌；令牌保存于本机 Keychain，服务端仅保存其 SHA-256 哈希。")
                }

                Section("连接状态") {
                    LabeledContent("API") {
                        Label(
                            store.isConnected ? "已连接" : "未连接",
                            systemImage: store.isConnected ? "checkmark.circle.fill" : "xmark.circle.fill"
                        )
                        .foregroundStyle(store.isConnected ? .green : .red)
                    }
                    LabeledContent("当前月份", value: store.selectedMonth)
                    if let settings = store.settings {
                        LabeledContent("预测长度", value: "\(settings.forecastMonths) 个月")
                        LabeledContent("OPEX 回看", value: "\(settings.opexLookbackMonths) 个月")
                        LabeledContent("计算版本", value: settings.calculationVersion)
                    }
                }

                Section("安全") {
                    Label("第三方密钥只保存在 Go API 服务端", systemImage: "lock.shield.fill")
                    Label("业务接口要求 Bearer 设备令牌", systemImage: "key.horizontal.fill")
                    Label("金额使用 Int64 最小货币单位", systemImage: "number")
                    Label("不同币种持仓不会直接合计", systemImage: "coloncurrencysign")
                }
                .font(.subheadline)
            }
            .navigationTitle("设置")
            .appAlert()
        }
    }
}

struct MonthSelector: View {
    @Environment(AppStore.self) private var store

    var body: some View {
        HStack {
            Button {
                Task { await store.changeMonth(by: -1) }
            } label: {
                Image(systemName: "chevron.left")
            }
            Spacer()
            Text(store.selectedMonth)
                .font(.headline.monospacedDigit())
            Spacer()
            Button {
                Task { await store.changeMonth(by: 1) }
            } label: {
                Image(systemName: "chevron.right")
            }
        }
        .buttonStyle(.bordered)
        .disabled(store.isLoading)
    }
}

struct RefreshToolbar: ToolbarContent {
    @Environment(AppStore.self) private var store

    var body: some ToolbarContent {
        ToolbarItem(placement: .primaryAction) {
            Button {
                Task { await store.refreshAll() }
            } label: {
                if store.isLoading {
                    ProgressView()
                } else {
                    Label("刷新", systemImage: "arrow.clockwise")
                }
            }
            .disabled(store.isLoading)
        }
    }
}

struct ConnectionBanner: View {
    @Environment(AppStore.self) private var store

    var body: some View {
        if !store.isConnected && store.hasLoaded {
            Label("未连接本地 API，请在设置中检查地址", systemImage: "wifi.exclamationmark")
                .font(.subheadline)
                .foregroundStyle(.red)
                .frame(maxWidth: .infinity, alignment: .leading)
                .padding()
                .background(.red.opacity(0.1), in: .rect(cornerRadius: 14))
        }
    }
}

struct MetricCard: View {
    let title: String
    let value: String
    let icon: String
    let tint: Color

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            Image(systemName: icon)
                .foregroundStyle(tint)
                .font(.title2)
            Text(title)
                .font(.caption)
                .foregroundStyle(.secondary)
            Text(value)
                .font(.headline.monospacedDigit())
                .minimumScaleFactor(0.65)
                .lineLimit(1)
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding()
        .background(.background.secondary, in: .rect(cornerRadius: 16))
    }
}

struct StatusHeader: View {
    let title: String
    let status: String
    let quality: String

    var body: some View {
        HStack {
            VStack(alignment: .leading, spacing: 4) {
                Text(title).font(.headline)
                Text(quality).font(.caption).foregroundStyle(.secondary)
            }
            Spacer()
            StatusPill(text: status, tint: status == "CLOSED" ? .green : .blue)
        }
        .padding()
        .background(.background.secondary, in: .rect(cornerRadius: 16))
    }
}

struct StatusPill: View {
    let text: String
    var tint: Color = .blue

    var body: some View {
        Text(text)
            .font(.caption2.weight(.semibold))
            .padding(.horizontal, 8)
            .padding(.vertical, 4)
            .foregroundStyle(tint)
            .background(tint.opacity(0.12), in: .capsule)
    }
}

struct SummaryBadge: View {
    let title: String
    let value: Int

    var body: some View {
        VStack(spacing: 4) {
            Text("\(value)").font(.title3.bold())
            Text(title).font(.caption).foregroundStyle(.secondary)
        }
        .frame(maxWidth: .infinity)
        .padding(.vertical, 12)
        .background(.background.secondary, in: .rect(cornerRadius: 12))
    }
}

enum LedgerSegment: String, CaseIterable, Identifiable {
    case provisional
    case official
    var id: Self { self }
    var title: String { self == .provisional ? "待校准" : "正式账本" }
}

enum StatementSort: String, CaseIterable, Identifiable {
    case date
    case amountAscending
    case amountDescending
    var id: Self { self }
    var title: String {
        switch self {
        case .date: "日期"
        case .amountAscending: "金额升序"
        case .amountDescending: "金额降序"
        }
    }
}

enum TrendMetric: String, CaseIterable, Identifiable {
    case opex
    case fcf
    case assets
    var id: Self { self }
    var title: String {
        switch self {
        case .opex: "OPEX"
        case .fcf: "FCF"
        case .assets: "总资产"
        }
    }
    func value(_ point: TrendPoint) -> Int64? {
        switch self {
        case .opex: point.opexMinor
        case .fcf: point.fcfMinor
        case .assets: point.endingInvestableAssetsMinor
        }
    }
}

private extension View {
    func appAlert() -> some View {
        modifier(AppAlertModifier())
    }
}

private struct AppAlertModifier: ViewModifier {
    @Environment(AppStore.self) private var store

    func body(content: Content) -> some View {
        content.alert(
            store.alert?.title ?? "",
            isPresented: Binding(
                get: { store.alert != nil },
                set: { if !$0 { store.dismissAlert() } }
            ),
            presenting: store.alert
        ) { _ in
            Button("好") { store.dismissAlert() }
        } message: { alert in
            Text(alert.message)
        }
    }
}

private extension Sequence where Element: Hashable {
    func uniqued() -> [Element] {
        var seen = Set<Element>()
        return filter { seen.insert($0).inserted }
    }
}
