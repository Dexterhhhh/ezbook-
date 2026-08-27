import SwiftUI

struct ContentView: View {
    @Environment(AppStore.self) private var store
    @State private var selection: AppSection = .overview

    var body: some View {
        TabView(selection: $selection) {
            Tab("概览", systemImage: "chart.pie.fill", value: .overview) {
                OverviewView()
            }
            Tab("记账", systemImage: "book.pages.fill", value: .ledger) {
                LedgerView()
            }
            Tab("账单", systemImage: "doc.text.fill", value: .statements) {
                StatementsView()
            }
            Tab("趋势", systemImage: "chart.xyaxis.line", value: .trends) {
                TrendsView()
            }
            Tab("投资", systemImage: "chart.line.uptrend.xyaxis", value: .investments) {
                InvestmentsView()
            }
            Tab("设置", systemImage: "gearshape.fill", value: .settings) {
                SettingsView()
            }
        }
        .task {
            guard !store.hasLoaded else { return }
            await store.refreshAll()
        }
    }
}

enum AppSection: Hashable {
    case overview
    case ledger
    case statements
    case trends
    case investments
    case settings
}

#Preview {
    ContentView()
        .environment(AppStore.preview)
}
