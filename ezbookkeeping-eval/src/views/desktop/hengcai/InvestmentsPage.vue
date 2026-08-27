<template>
    <div>
		<v-card class="mb-4">
            <v-tabs v-model="pageTab" color="primary" grow>
				<v-tab value="overview">持仓与盈亏</v-tab>
                <v-tab value="portfolio">投资管理</v-tab>
                <v-tab value="reconciliation">投资对账</v-tab>
            </v-tabs>
        </v-card>

		<div v-show="pageTab === 'overview'">
			<v-card class="mb-4">
				<v-card-title class="investment-header">
					<div>
						<div>持仓与盈亏</div>
						<div class="text-body-2 text-medium-emphasis mt-1">集中查看当前持仓、市值和盈亏；手动同步可随时获取 Alpaca 最新可用价格</div>
					</div>
					<div class="market-actions">
						<v-select v-model="feed" class="feed-select" density="compact" hide-details label="行情源" :items="['iex', 'sip']" />
						<v-btn color="primary" :loading="syncing" :disabled="!data.positions.length" @click="syncPrices">同步当前价格</v-btn>
					</div>
				</v-card-title>
			<v-card-text>
				<v-alert v-if="message" :type="messageType" variant="tonal" class="mb-4">{{ message }}</v-alert>
				<v-alert v-if="unlinkedAccounts.length" type="warning" variant="tonal" class="mb-4">
					<div class="d-flex align-center justify-space-between ga-4 flex-wrap">
						<span>有 {{ unlinkedAccounts.length }} 个投资账户尚未关联主账本，资产总览暂时不会包含这些账户。</span>
						<v-btn color="warning" variant="flat" size="small" :loading="linkingKey === 'all'" @click="linkAllAccounts">为全部创建汇总账户</v-btn>
					</div>
				</v-alert>
					<v-row v-if="portfolioSummaries.length">
						<v-col v-for="summary in portfolioSummaries" :key="summary.currency" cols="12" md="6" lg="4">
							<v-card variant="outlined" class="summary-card">
								<div class="d-flex align-center justify-space-between mb-3"><span class="text-subtitle-1">{{ summary.currency }} 持仓</span><v-chip size="small" variant="tonal">{{ summary.count }} 项</v-chip></div>
								<div class="summary-value">{{ money(summary.marketValueMinor, summary.currency) }}</div>
								<div class="text-caption text-medium-emphasis mb-3">当前市值</div>
								<div class="d-flex justify-space-between ga-4"><span>成本 {{ money(summary.costValueMinor, summary.currency) }}</span><span :class="pnlClass(summary.unrealizedPnlMinor)">未实现 {{ money(summary.unrealizedPnlMinor, summary.currency) }}</span></div>
							</v-card>
						</v-col>
					</v-row>
					<v-alert v-else type="info" variant="tonal">暂无持仓；导入对账单或录入投资交易后会自动生成。</v-alert>
				</v-card-text>
			</v-card>

			<v-card class="mb-4">
				<v-card-title>当前持仓</v-card-title>
				<v-table density="comfortable">
					<thead><tr><th>账户</th><th>标的</th><th class="text-end">数量</th><th class="text-end">成本</th><th class="text-end">最新价</th><th>价格时间</th><th class="text-end">持仓价值</th><th class="text-end">未实现盈亏</th><th class="text-end">收益率</th></tr></thead>
					<tbody>
						<tr v-if="!data.positions.length"><td colspan="9" class="text-center py-6">暂无持仓</td></tr>
						<tr v-for="item in data.positions" :key="item.id"><td>{{ accountName(item.investment_account_id) }}</td><td>{{ instrumentName(item.instrument_id) }}</td><td class="text-end">{{ item.quantity }}</td><td class="text-end">{{ money(item.cost_value_minor, instrumentCurrency(item.instrument_id)) }}</td><td class="text-end">{{ item.market_price > 0 ? item.market_price : '未同步' }}</td><td>{{ priceTime(item.instrument_id) }}</td><td class="text-end">{{ money(item.market_value_minor, instrumentCurrency(item.instrument_id)) }}</td><td class="text-end" :class="pnlClass(item.unrealized_pnl_minor)">{{ money(item.unrealized_pnl_minor, instrumentCurrency(item.instrument_id)) }}</td><td class="text-end">{{ percent(item.return_bps) }}</td></tr>
					</tbody>
				</v-table>
			</v-card>

			<v-card>
				<v-card-title>月度盈亏</v-card-title>
				<v-table density="comfortable">
					<thead><tr><th>月份</th><th class="text-end">已实现</th><th class="text-end">未实现</th><th class="text-end">总收益</th><th class="text-end">收益率</th><th>数据质量</th></tr></thead>
					<tbody>
						<tr v-if="!data.returns.length"><td colspan="6" class="text-center py-6">暂无收益数据</td></tr>
						<tr v-for="item in data.returns" :key="item.id"><td>{{ item.month }}</td><td class="text-end">{{ money(item.realized_pnl_minor) }}</td><td class="text-end">{{ money(item.unrealized_pnl_minor) }}</td><td class="text-end" :class="pnlClass(item.total_return_minor)">{{ money(item.total_return_minor) }}</td><td class="text-end">{{ percent(item.return_bps) }}</td><td>{{ returnQuality(item.quality) }}</td></tr>
					</tbody>
				</v-table>
			</v-card>
		</div>

        <div v-show="pageTab === 'portfolio'">
        <v-card class="mb-4">
            <v-card-title class="investment-header">
                <div>
					<div>投资管理</div>
					<div class="text-body-2 text-medium-emphasis mt-1">维护投资账户、标的和交易记录</div>
                </div>
                <div class="market-actions">
                    <v-select v-model="feed" class="feed-select" density="compact" hide-details label="行情源" :items="['iex', 'sip']" />
					<v-btn color="primary" :loading="syncing" :disabled="!data.positions.length" @click="syncPrices">同步当前价格</v-btn>
                </div>
            </v-card-title>
            <v-card-text>
                <v-alert type="info" variant="tonal" class="mb-4">
					正确顺序：先建立投资账户和投资标的，再录入交易。持仓由交易自动计算；手动同步会立即获取 Alpaca 最新可用价格。
                </v-alert>
                <v-alert v-if="message" :type="messageType" variant="tonal" class="mb-4">{{ message }}</v-alert>
                <v-card variant="outlined" class="entry-card">
                    <v-tabs v-model="entryTab" grow color="primary">
                        <v-tab value="account">新增投资账户</v-tab>
                        <v-tab value="instrument">新增投资标的</v-tab>
                        <v-tab value="transaction" :disabled="!canCreateTransaction">新增投资交易</v-tab>
                    </v-tabs>
                    <v-divider />
                    <v-card-text class="entry-form">
                        <v-window v-model="entryTab">
                            <v-window-item value="account">
                                <v-form @submit.prevent="addAccount">
                                    <v-row>
                                        <v-col cols="12" md="6"><v-text-field v-model="accountForm.name" label="账户名称" autocomplete="off" required /></v-col>
                                        <v-col cols="12" md="6"><v-text-field v-model="accountForm.institution" label="券商 / 机构" autocomplete="off" /></v-col>
                                        <v-col cols="12" md="4"><v-select v-model="accountForm.account_type" label="账户类型" :items="accountTypes" /></v-col>
                                        <v-col cols="12" md="4"><v-select v-model="accountForm.base_currency" label="基础币种" :items="currencies" /></v-col>
                                        <v-col cols="12" md="4"><v-select v-model="accountForm.account_id" label="关联主账本资产账户" :items="coreAccountItems" item-title="title" item-value="value" /></v-col>
                                        <v-col cols="12"><v-btn type="submit" color="primary" :loading="savingKind === 'account'" :disabled="!accountForm.name.trim()">保存账户</v-btn></v-col>
                                    </v-row>
                                </v-form>
                            </v-window-item>
                            <v-window-item value="instrument">
                                <v-form @submit.prevent="addInstrument">
                                    <v-row>
                                        <v-col cols="12" md="6"><v-text-field v-model="instrumentForm.symbol" label="代码（如 AAPL）" autocomplete="off" required /></v-col>
                                        <v-col cols="12" md="6"><v-text-field v-model="instrumentForm.name" label="标的名称" autocomplete="off" required /></v-col>
                                        <v-col cols="12" md="4"><v-select v-model="instrumentForm.market" label="市场" :items="markets" /></v-col>
                                        <v-col cols="12" md="4"><v-select v-model="instrumentForm.asset_type" label="资产类型" :items="assetTypes" /></v-col>
                                        <v-col cols="12" md="4"><v-select v-model="instrumentForm.currency" label="币种" :items="currencies" /></v-col>
                                        <v-col cols="12"><v-btn type="submit" color="primary" :loading="savingKind === 'instrument'" :disabled="!instrumentForm.symbol.trim() || !instrumentForm.name.trim()">保存标的</v-btn></v-col>
                                    </v-row>
                                </v-form>
                            </v-window-item>
                            <v-window-item value="transaction">
                                <v-alert v-if="!canCreateTransaction" type="warning" variant="tonal" class="mb-4">必须至少有一个投资账户和一个投资标的，才能录入交易。</v-alert>
                                <v-form @submit.prevent="addTransaction">
                                    <v-row>
                                        <v-col cols="12" md="4"><v-select v-model="transactionForm.investment_account_id" label="投资账户" :items="accountItems" item-title="title" item-value="value" :disabled="!data.accounts.length" required /></v-col>
                                        <v-col cols="12" md="4"><v-select v-model="transactionForm.instrument_id" label="投资标的" :items="instrumentItems" item-title="title" item-value="value" :disabled="!data.instruments.length" required /></v-col>
                                        <v-col cols="12" md="4"><v-select v-model="transactionForm.action" label="交易动作" :items="actionItems" item-title="title" item-value="value" /></v-col>
                                        <v-col cols="12" md="4"><v-text-field v-model.number="transactionForm.quantity" label="数量" type="number" min="0" step="any" required /></v-col>
                                        <v-col cols="12" md="4"><v-text-field v-model.number="transactionForm.price" label="成交价（每份）" type="number" min="0" step="any" required /></v-col>
                                        <v-col cols="12" md="4"><v-text-field v-model.number="transactionForm.fees_minor" label="费用（最小货币单位）" type="number" min="0" /></v-col>
                                        <v-col cols="12"><v-btn type="submit" color="primary" :loading="savingKind === 'transaction'" :disabled="!transactionReady">保存交易</v-btn></v-col>
                                    </v-row>
                                </v-form>
                            </v-window-item>
                        </v-window>
                    </v-card-text>
                </v-card>
            </v-card-text>
        </v-card>

		<v-card class="mb-4">
			<v-card-title class="d-flex align-center justify-space-between ga-4 flex-wrap">
				<span>投资账户与主账本汇总</span>
				<v-btn v-if="unlinkedAccounts.length" color="primary" variant="tonal" size="small" :loading="linkingKey === 'all'" @click="linkAllAccounts">关联全部未关联账户</v-btn>
			</v-card-title>
			<v-list density="comfortable">
				<v-list-item v-if="!data.accounts.length" title="暂无投资账户，请先新增" />
				<v-list-item v-for="item in data.accounts" :key="item.id" :title="item.name" :subtitle="accountSubtitle(item)">
					<template #append>
						<div class="account-link-controls">
							<v-select v-model="linkSelections[item.id]" class="account-link-select" density="compact" hide-details label="主账本汇总账户" :items="linkItemsFor(item)" item-title="title" item-value="value" />
							<v-btn color="primary" variant="tonal" size="small" :loading="linkingKey === `account:${item.id}`" @click="saveAccountLink(item)">保存关联</v-btn>
							<v-chip v-if="duplicateAccountIds.has(item.id)" color="warning" size="small">重复账户</v-chip>
							<v-btn color="error" variant="text" size="small" :loading="deletingKey === `account:${item.id}`" @click="removeInvestment('account', item)">删除</v-btn>
						</div>
                    </template>
                </v-list-item>
            </v-list>
        </v-card>

        <v-card class="mb-4">
            <v-card-title>投资标的</v-card-title>
            <v-table density="comfortable">
				<thead><tr><th>代码</th><th>名称</th><th>合约详情</th><th>市场</th><th>类型</th><th>币种</th><th>最新价格</th><th class="text-end">操作</th></tr></thead>
                <tbody>
                    <tr v-if="!data.instruments.length"><td colspan="8" class="text-center py-6">暂无投资标的，请先新增</td></tr>
                    <tr v-for="item in data.instruments" :key="item.id"><td><div>{{ item.symbol }}</div><div v-if="item.asset_type === 'OPTION'" class="text-caption text-medium-emphasis">券商合约代码</div></td><td>{{ item.name }}</td><td>{{ optionDetails(item) }}</td><td>{{ item.market }}</td><td>{{ item.asset_type }}</td><td>{{ item.currency }}</td><td>{{ latestPrice(item.id) }}</td><td class="text-end"><v-btn color="error" variant="text" size="small" :loading="deletingKey === `instrument:${item.id}`" @click="removeInvestment('instrument', item)">删除</v-btn></td></tr>
                </tbody>
            </v-table>
        </v-card>

        <v-card>
            <v-card-title>交易记录</v-card-title>
            <v-table density="comfortable">
                <thead><tr><th>时间</th><th>动作</th><th>账户</th><th>标的</th><th class="text-end">数量</th><th class="text-end">成交价</th><th class="text-end">净现金流</th><th>来源</th><th class="text-end">操作</th></tr></thead>
                <tbody>
                    <tr v-if="!data.transactions.length"><td colspan="9" class="text-center py-6">暂无交易记录</td></tr>
                    <tr v-for="item in data.transactions" :key="item.id"><td>{{ formatTime(item.traded_at) }}</td><td>{{ item.action === 'BUY' ? '买入' : '卖出' }}</td><td>{{ accountName(item.investment_account_id) }}</td><td>{{ instrumentName(item.instrument_id) }}</td><td class="text-end">{{ item.quantity }}</td><td class="text-end">{{ item.price }}</td><td class="text-end">{{ money(item.net_cash_amount_minor, item.currency) }}</td><td>{{ item.source }}</td><td class="text-end"><v-btn color="error" variant="text" size="small" :loading="deletingKey === `transaction:${item.id}`" @click="removeInvestment('transaction', item)">删除</v-btn></td></tr>
                </tbody>
            </v-table>
        </v-card>
        </div>

        <div v-show="pageTab === 'reconciliation'">
            <v-card class="mb-4">
                <v-card-title>券商 PDF 对账</v-card-title>
                <v-card-subtitle>支持汇丰、IBKR、致富证券香港及致富证券全球账单；可手动指定识别引擎</v-card-subtitle>
                <v-card-text>
                    <v-alert type="info" variant="tonal" class="mb-4">
                        先预览并通过交易、持仓和资金完整性校验，再确认入账。确认后，该投资账户在结单周期内的原交易会被对账单交易替换，期末持仓以对账单为准。
                    </v-alert>
                    <v-alert v-if="message" :type="messageType" variant="tonal" class="mb-4">{{ message }}</v-alert>
                    <v-row>
                        <v-col cols="12" md="3"><v-select v-model="reconciliationEngine" label="券商识别引擎" :items="reconciliationEngineItems" item-title="title" item-value="value" /></v-col>
                        <v-col cols="12" md="5"><v-file-input v-model="statementFiles" label="选择券商 PDF 对账单" accept="application/pdf,.pdf" prepend-icon="mdi-file-pdf-box" show-size /></v-col>
                        <v-col cols="12" md="4"><v-select v-model="reconciliationAccountId" label="对应投资账户" :items="reconciliationAccountItems" item-title="title" item-value="value" :disabled="!reconciliationAccountItems.length" /></v-col>
                        <v-col cols="12" md="4"><v-text-field v-model="statementPassword" label="PDF 密码（如有）" type="password" autocomplete="off" clearable /></v-col>
                        <v-col cols="12" class="d-flex ga-3 flex-wrap">
                            <v-btn color="primary" variant="tonal" :loading="previewingStatement" :disabled="!selectedStatementFile" @click="previewStatement">识别并预览</v-btn>
                            <v-btn color="primary" :loading="confirmingStatement" :disabled="!statementPreview?.ready || !reconciliationAccountId || !reconciliationCurrencyMatches" @click="confirmStatement">确认对账并替换周期交易</v-btn>
                        </v-col>
                    </v-row>
                    <v-alert v-if="statementPreview?.ready && !reconciliationCurrencyMatches" type="warning" variant="tonal" class="mt-4">
                        对账单基础币种为 {{ statementPreview.base_currency }}，请选择相同币种的投资账户后再确认。
                    </v-alert>
                </v-card-text>
            </v-card>

            <template v-if="statementPreview">
                <v-card class="mb-4">
                    <v-card-title class="d-flex align-center ga-3 flex-wrap">
                        对账预览
                        <v-chip :color="statementPreview.ready ? 'success' : 'error'" size="small">{{ statementPreview.ready ? '完整性校验通过' : '需要检查' }}</v-chip>
                    </v-card-title>
                    <v-card-text>
                        <v-row>
                            <v-col cols="6" md="3"><div class="text-caption text-medium-emphasis">券商</div><div>{{ providerName(statementPreview.provider) }}</div></v-col>
                            <v-col cols="6" md="3"><div class="text-caption text-medium-emphasis">券商账户</div><div>{{ maskBrokerAccount(statementPreview.account_number) }}</div></v-col>
                            <v-col cols="6" md="3"><div class="text-caption text-medium-emphasis">结单周期</div><div>{{ statementPreview.period_start }} 至 {{ statementPreview.period_end }}</div></v-col>
                            <v-col cols="6" md="3"><div class="text-caption text-medium-emphasis">基础币种</div><div>{{ statementPreview.base_currency }}</div></v-col>
                            <v-col cols="6" md="3"><div class="text-caption text-medium-emphasis">期末净值</div><div>{{ money(statementPreview.closing_net_assets_minor || statementPreview.portfolio_value_minor, statementPreview.base_currency) }}</div></v-col>
                            <v-col cols="6" md="3"><div class="text-caption text-medium-emphasis">组合市值</div><div>{{ money(statementPreview.portfolio_value_minor, statementPreview.base_currency) }}</div></v-col>
                            <v-col cols="6" md="3"><div class="text-caption text-medium-emphasis">费用</div><div>{{ money(statementPreview.fees_minor, statementPreview.base_currency) }}</div></v-col>
                            <v-col cols="6" md="3"><div class="text-caption text-medium-emphasis">交易 / 持仓</div><div>{{ statementPreview.trades.length }} / {{ statementPreview.holdings.length }}</div></v-col>
                        </v-row>
                        <v-alert v-if="statementPreview.validation_errors?.length" type="error" variant="tonal" class="mt-4">{{ statementPreview.validation_errors.join('；') }}</v-alert>
                    </v-card-text>
                </v-card>

                <v-card class="mb-4">
                    <v-card-title>账单交易</v-card-title>
                    <v-table density="comfortable">
                        <thead><tr><th>日期</th><th>标的 / 期权合约</th><th>动作</th><th class="text-end">数量</th><th class="text-end">成交价</th><th class="text-end">费用</th><th class="text-end">净现金流</th><th>交易编号</th></tr></thead>
                        <tbody><tr v-for="(item, index) in statementPreview.trades" :key="`${item.symbol}-${item.trade_date}-${index}`"><td>{{ item.trade_date }}</td><td><div>{{ statementTradeLabel(item) }}</div><div v-if="item.asset_type === 'OPTION'" class="text-caption text-medium-emphasis">券商代码 {{ item.symbol }} · 乘数 {{ item.contract_multiplier }}</div></td><td>{{ item.action === 'BUY' ? '买入' : '卖出' }}</td><td class="text-end">{{ item.quantity }}</td><td class="text-end">{{ item.price }}</td><td class="text-end">{{ money(item.fees_minor + item.taxes_minor, item.currency) }}</td><td class="text-end">{{ money(item.net_cash_amount_minor, item.currency) }}</td><td>{{ item.external_reference || '—' }}</td></tr></tbody>
                    </v-table>
                </v-card>

                <v-card class="mb-4">
                    <v-card-title>期末持仓核对</v-card-title>
                    <v-table density="comfortable">
                        <thead><tr><th>代码</th><th>名称</th><th class="text-end">期初数量</th><th class="text-end">期末数量</th><th class="text-end">账单收盘价</th><th class="text-end">市值</th><th class="text-end">未实现盈亏</th></tr></thead>
                        <tbody><tr v-for="item in statementPreview.holdings" :key="item.symbol"><td>{{ item.symbol }}</td><td>{{ item.name }}</td><td class="text-end">{{ item.opening_quantity }}</td><td class="text-end">{{ item.closing_quantity }}</td><td class="text-end">{{ item.closing_price }}</td><td class="text-end">{{ money(item.market_value_minor, item.currency) }}</td><td class="text-end">{{ item.unrealized_pnl_minor ? money(item.unrealized_pnl_minor, item.currency) : '—' }}</td></tr></tbody>
                    </v-table>
                </v-card>
            </template>

            <v-card>
                <v-card-title>对账历史</v-card-title>
                <v-table density="comfortable">
                    <thead><tr><th>结单周期</th><th>券商</th><th>投资账户</th><th>状态</th><th class="text-end">交易</th><th class="text-end">持仓</th><th class="text-end">替换旧交易</th><th class="text-end">期初调整</th><th>确认时间</th></tr></thead>
                    <tbody>
                        <tr v-if="!reconciliations.length"><td colspan="9" class="text-center py-6">暂无已确认的投资对账</td></tr>
                        <tr v-for="item in reconciliations" :key="item.id"><td>{{ item.period_start }} 至 {{ item.period_end }}</td><td>{{ providerName(item.provider) }}</td><td>{{ accountName(item.investment_account_id) }}</td><td><v-chip :color="item.status === 'RECONCILED' ? 'success' : 'default'" size="small">{{ item.status === 'RECONCILED' ? '已对账' : '已被替代' }}</v-chip></td><td class="text-end">{{ item.trade_count }}</td><td class="text-end">{{ item.holding_count }}</td><td class="text-end">{{ item.replaced_transaction_count }}</td><td class="text-end">{{ item.opening_adjustment_count }}</td><td>{{ formatTime(item.created_unix_time) }}</td></tr>
                    </tbody>
                </v-table>
            </v-card>
        </div>
        <confirm-dialog ref="confirmDialog" />
    </div>
</template>

<script setup lang="ts">
import axios from 'axios';
import { computed, onMounted, ref, useTemplateRef, watch } from 'vue';

import ConfirmDialog from '@/components/desktop/ConfirmDialog.vue';
import { useAccountsStore } from '@/stores/account.ts';

type InvestmentAccount = { id: number; name: string; institution: string; account_type: string; base_currency: string; account_id: string; active: boolean };
type InvestmentValuation = { investment_account_id: number; base_currency: string; position_value_minor: number; total_equity_minor: number; source: string; quality: string; as_of_unix_time: number };
type InvestmentInstrument = { id: number; symbol: string; name: string; market: string; asset_type: string; currency: string; underlying_symbol: string; expiration_date: string; option_type: string; strike_price: number; contract_multiplier: number };
type InvestmentData = { accounts: InvestmentAccount[]; instruments: InvestmentInstrument[]; transactions: any[]; positions: any[]; valuations: InvestmentValuation[]; returns: any[] };
type CoreAccount = { id: string; name: string; currency: string; category: number; type: number; isAsset?: boolean; hidden?: boolean; subAccounts?: CoreAccount[] };
type ConfirmDialogType = InstanceType<typeof ConfirmDialog>;

const accountsStore = useAccountsStore();
const data = ref<InvestmentData>({ accounts: [], instruments: [], transactions: [], positions: [], valuations: [], returns: [] });
const coreAccounts = ref<CoreAccount[]>([]);
const prices = ref<any[]>([]);
const pageTab = ref<'overview' | 'portfolio' | 'reconciliation'>('overview');
const entryTab = ref<'account' | 'instrument' | 'transaction'>('account');
const feed = ref('iex');
const syncing = ref(false);
const savingKind = ref('');
const deletingKey = ref('');
const linkingKey = ref('');
const linkSelections = ref<Record<number, string>>({});
const message = ref('');
const messageType = ref<'success' | 'error' | 'warning'>('success');
const confirmDialog = useTemplateRef<ConfirmDialogType>('confirmDialog');
const statementFiles = ref<File[] | File | null>(null);
const statementPassword = ref('');
const reconciliationEngine = ref('AUTO');
const statementPreview = ref<any | null>(null);
const reconciliationAccountId = ref<number | null>(null);
const reconciliations = ref<any[]>([]);
const previewingStatement = ref(false);
const confirmingStatement = ref(false);

const accountTypes = ['BROKERAGE', 'RETIREMENT', 'CRYPTO', 'OTHER'];
const assetTypes = ['STOCK', 'ETF', 'OPTION', 'FUND', 'BOND', 'CRYPTO', 'OTHER'];
const markets = ['US', 'HK', 'CN', 'CRYPTO', 'OTHER'];
const currencies = ['USD', 'CNY', 'HKD', 'EUR', 'JPY', 'GBP'];
const actionItems = [{ title: '买入', value: 'BUY' }, { title: '卖出', value: 'SELL' }];
const providerNames: Record<string, string> = { HSBC: '汇丰', IBKR: 'IBKR', CHIEF_HK: '致富证券香港', CHIEF_GLOBAL: '致富证券全球' };
const reconciliationEngineItems = [
    { title: '自动识别', value: 'AUTO' },
    { title: '汇丰投资综合结单', value: 'HSBC' },
    { title: 'IBKR 活动账单', value: 'IBKR' },
    { title: '致富证券香港月结单', value: 'CHIEF_HK' },
    { title: '致富证券全球月结单', value: 'CHIEF_GLOBAL' }
];

const newAccountForm = () => ({ name: '', institution: '', account_type: 'BROKERAGE', base_currency: 'USD', account_id: '0' });
const newInstrumentForm = () => ({ asset_type: 'STOCK', market: 'US', symbol: '', name: '', currency: 'USD', contract_key: '', price_scale: 4, quantity_scale: 6 });
const accountForm = ref(newAccountForm());
const instrumentForm = ref(newInstrumentForm());
const transactionForm = ref({ investment_account_id: null as number | null, instrument_id: null as number | null, traded_at: 0, action: 'BUY', quantity: 0, price: 0, fees_minor: 0, taxes_minor: 0, source: 'MANUAL' });

const accountItems = computed(() => data.value.accounts.filter(item => item.active).map(item => ({ title: `${item.name} (#${item.id})`, value: item.id })));
const reconciliationAccountItems = computed(() => data.value.accounts.filter(item => item.active).map(item => ({ title: `${item.name} · ${item.base_currency} (#${item.id})`, value: item.id })));
const instrumentItems = computed(() => data.value.instruments.map(item => ({ title: `${item.symbol} · ${item.name}`, value: item.id })));
const canCreateTransaction = computed(() => accountItems.value.length > 0 && instrumentItems.value.length > 0);
const transactionReady = computed(() => canCreateTransaction.value && !!transactionForm.value.investment_account_id && !!transactionForm.value.instrument_id && transactionForm.value.quantity > 0 && transactionForm.value.price > 0);
const linkedCoreAccountIds = computed(() => new Set(data.value.accounts.filter(item => item.account_id && item.account_id !== '0').map(item => item.account_id)));
const compatibleCoreAccounts = computed(() => coreAccounts.value.filter(item => item.category === 7 && item.type === 1));
const coreAccountItems = computed(() => [{ title: '自动创建同名投资汇总账户', value: '0' }, ...compatibleCoreAccounts.value.filter(item => !linkedCoreAccountIds.value.has(item.id)).map(item => ({ title: `${item.name} · ${item.currency}`, value: item.id }))]);
const unlinkedAccounts = computed(() => data.value.accounts.filter(item => !item.account_id || item.account_id === '0'));
const duplicateAccountIds = computed(() => {
    const groups = new Map<string, number[]>();
    data.value.accounts.forEach(item => { const key = `${item.name.trim().toLowerCase()}|${(item.institution || '').trim().toLowerCase()}`; groups.set(key, [...(groups.get(key) || []), item.id]); });
    return new Set([...groups.values()].filter(ids => ids.length > 1).flat());
});
const portfolioSummaries = computed(() => {
	const groups = new Map<string, { currency: string; count: number; costValueMinor: number; marketValueMinor: number; unrealizedPnlMinor: number }>();
	for (const position of data.value.positions) {
		const currency = instrumentCurrency(position.instrument_id);
		const summary = groups.get(currency) || { currency, count: 0, costValueMinor: 0, marketValueMinor: 0, unrealizedPnlMinor: 0 };
		summary.count += 1;
		summary.costValueMinor += Number(position.cost_value_minor || 0);
		summary.marketValueMinor += Number(position.market_value_minor || 0);
		summary.unrealizedPnlMinor += Number(position.unrealized_pnl_minor || 0);
		groups.set(currency, summary);
	}
	return [...groups.values()].sort((left, right) => left.currency.localeCompare(right.currency));
});
const selectedStatementFile = computed<File | null>(() => Array.isArray(statementFiles.value) ? statementFiles.value[0] || null : statementFiles.value || null);
const reconciliationCurrencyMatches = computed(() => {
    if (!statementPreview.value || !reconciliationAccountId.value) return true;
    const account = data.value.accounts.find(item => item.id === reconciliationAccountId.value);
    return !!account && account.base_currency.toUpperCase() === String(statementPreview.value.base_currency || '').toUpperCase();
});

function flattenAccounts(items: CoreAccount[]): CoreAccount[] {
    return items.flatMap(item => [item, ...flattenAccounts(item.subAccounts || [])]);
}
function coreAccountName(id: string): string {
    if (!id || id === '0') return '未关联主账本';
    return coreAccounts.value.find(item => item.id === id)?.name || '关联账户不可用';
}
function accountValuation(id: number): InvestmentValuation | undefined {
    return data.value.valuations.find(item => item.investment_account_id === id);
}
function valuationQuality(value?: InvestmentValuation): string {
    if (!value) return '尚未生成估值';
    if (value.quality === 'COMPLETE') return '行情与账单完整';
    if (value.quality === 'COST_BASED') return '部分标的按成本估值';
    if (value.quality === 'FX_REQUIRED' || value.quality === 'PARTIAL_FX_REQUIRED') return '多币种，暂按最近账单汇总';
    return '仅持仓汇总';
}
function accountSubtitle(item: InvestmentAccount): string {
    const valuation = accountValuation(item.id);
    const total = valuation ? money(valuation.total_equity_minor, valuation.base_currency) : '等待估值';
    return `${item.institution || '未设置机构'} · ${item.account_type} · ${item.base_currency} · ${coreAccountName(item.account_id)} · 汇总 ${total}（${valuationQuality(valuation)}）`;
}
function linkItemsFor(item: InvestmentAccount): Array<{ title: string; value: string }> {
    const items = [{ title: '自动创建同名投资汇总账户', value: 'AUTO' }, { title: '暂不关联', value: '0' }];
    for (const coreAccount of compatibleCoreAccounts.value) {
        const usedByOther = linkedCoreAccountIds.value.has(coreAccount.id) && coreAccount.id !== item.account_id;
        if (!usedByOther && coreAccount.currency.toUpperCase() === item.base_currency.toUpperCase()) {
            items.push({ title: `${coreAccount.name} · ${coreAccount.currency}`, value: coreAccount.id });
        }
    }
    return items;
}
const accountName = (id: number): string => data.value.accounts.find(item => item.id === id)?.name || `账户 #${id}`;
const optionDirection = (value: string): string => value === 'PUT' ? '看跌' : value === 'CALL' ? '看涨' : value || '期权';
const optionDetails = (item: any): string => item?.asset_type === 'OPTION' ? `${item.underlying_symbol} · ${item.expiration_date} · ${optionDirection(item.option_type)} · 行权价 ${item.strike_price} · ×${item.contract_multiplier || 100}` : '—';
const statementTradeLabel = (item: any): string => item?.asset_type === 'OPTION' ? `${item.underlying_symbol} ${item.expiration_date} ${optionDirection(item.option_type)} ${item.strike_price}` : item.symbol;
const instrumentName = (id: number): string => { const item = data.value.instruments.find(value => value.id === id); return item ? (item.asset_type === 'OPTION' ? statementTradeLabel(item) : item.symbol) : `标的 #${id}`; };
const instrumentCurrency = (id: number): string => data.value.instruments.find(item => item.id === id)?.currency || 'CNY';
const money = (minor: number, currency = 'CNY'): string => new Intl.NumberFormat('zh-CN', { style: 'currency', currency }).format((minor || 0) / 100);
const percent = (bps: number): string => `${((bps || 0) / 100).toFixed(2)}%`;
const latestPrice = (id: number): string => { const item = prices.value.find(price => price.instrument_id === id); return item ? `${item.close} · ${item.provider}` : '未同步'; };
const priceTime = (id: number): string => { const item = prices.value.find(price => price.instrument_id === id); return item?.as_of_unix_time ? new Date(item.as_of_unix_time * 1000).toLocaleString('zh-CN') : '未同步'; };
const pnlClass = (value: number): string => value > 0 ? 'text-success' : value < 0 ? 'text-error' : '';
const returnQuality = (quality: string): string => quality === 'MARKED' ? '已按最新行情估值' : quality === 'STATEMENT_AGGREGATED' ? '已按对账单汇总' : '尚未获取完整行情';
const formatTime = (unix: number): string => unix ? new Date(unix * 1000).toLocaleDateString('zh-CN') : '—';
const providerName = (provider: string): string => providerNames[provider] || provider;
const requestError = (ex: any, fallback: string): string => ex?.response?.data?.errorMessage || ex?.response?.data?.error?.message || fallback;

function chooseDefaults(): void {
    if (!accountItems.value.some(item => item.value === transactionForm.value.investment_account_id)) transactionForm.value.investment_account_id = accountItems.value[0]?.value || null;
    if (!instrumentItems.value.some(item => item.value === transactionForm.value.instrument_id)) transactionForm.value.instrument_id = instrumentItems.value[0]?.value || null;
    if (!accountItems.value.some(item => item.value === reconciliationAccountId.value)) reconciliationAccountId.value = accountItems.value[0]?.value || null;
}
async function load(): Promise<void> {
    const [investmentResponse, priceResponse, accountResponse, reconciliationResponse] = await Promise.all([
        axios.get('v1/hengcai/investments/list.json'),
        axios.get('v1/hengcai/market/prices.json'),
        axios.get('v1/accounts/list.json?visible_only=false'),
        axios.get('v1/hengcai/investments/reconciliation/list.json')
    ]);
    const result = investmentResponse.data.result || {};
    data.value = {
        accounts: Array.isArray(result.accounts) ? result.accounts : [],
        instruments: Array.isArray(result.instruments) ? result.instruments : [],
        transactions: Array.isArray(result.transactions) ? result.transactions : [],
        positions: Array.isArray(result.positions) ? result.positions : [],
        valuations: Array.isArray(result.valuations) ? result.valuations : [],
        returns: Array.isArray(result.returns) ? result.returns : []
    };
    prices.value = Array.isArray(priceResponse.data.result) ? priceResponse.data.result : [];
    coreAccounts.value = flattenAccounts(accountResponse.data.result || []);
    reconciliations.value = Array.isArray(reconciliationResponse.data.result) ? reconciliationResponse.data.result : [];
    for (const account of data.value.accounts) {
        linkSelections.value[account.id] = account.account_id && account.account_id !== '0' ? account.account_id : 'AUTO';
    }
    chooseDefaults();
}
async function refreshGlobalAccounts(): Promise<void> {
    try { await accountsStore.loadAllAccounts({ force: true }); } catch { /* up-to-date is not an error */ }
}
async function saveAccountLink(item: InvestmentAccount): Promise<void> {
    const selection = linkSelections.value[item.id] || 'AUTO';
    linkingKey.value = `account:${item.id}`;
    message.value = '';
    try {
        await axios.post('v1/hengcai/investments/link.json', {
            investment_account_id: item.id,
            account_id: selection === 'AUTO' ? '0' : selection,
            create_core_account: selection === 'AUTO'
        });
        await load();
        await refreshGlobalAccounts();
        messageType.value = 'success';
        message.value = selection === '0' ? `已解除“${item.name}”的主账本关联` : `“${item.name}”已关联主账本汇总账户`;
    } catch (ex: any) {
        messageType.value = 'error';
        message.value = requestError(ex, '主账本账户关联失败');
    } finally {
        linkingKey.value = '';
    }
}
async function linkAllAccounts(): Promise<void> {
    linkingKey.value = 'all';
    message.value = '';
    try {
        for (const account of unlinkedAccounts.value) {
            await axios.post('v1/hengcai/investments/link.json', { investment_account_id: account.id, account_id: '0', create_core_account: true });
        }
        await load();
        await refreshGlobalAccounts();
        messageType.value = 'success';
        message.value = '全部投资账户已创建并关联主账本汇总账户';
    } catch (ex: any) {
        messageType.value = 'error';
        message.value = requestError(ex, '批量关联未全部完成，请检查各账户状态');
    } finally {
        linkingKey.value = '';
    }
}
function statementFormData(): FormData | null {
    if (!selectedStatementFile.value) return null;
    const payload = new FormData();
    payload.append('file', selectedStatementFile.value);
    payload.append('engine', reconciliationEngine.value);
    if (statementPassword.value) payload.append('password', statementPassword.value);
    return payload;
}

watch([statementFiles, reconciliationEngine, statementPassword], () => {
    statementPreview.value = null;
    message.value = '';
});
async function previewStatement(): Promise<void> {
    const payload = statementFormData();
    if (!payload) return;
    previewingStatement.value = true;
    statementPreview.value = null;
    message.value = '';
    try {
        const response = await axios.post('v1/hengcai/investments/reconciliation/preview.json', payload);
        statementPreview.value = response.data.result?.statement || null;
        if (statementPreview.value?.base_currency && !reconciliationCurrencyMatches.value) {
            const matchingAccount = data.value.accounts.find(item => item.active && item.base_currency.toUpperCase() === statementPreview.value.base_currency.toUpperCase());
            if (matchingAccount) reconciliationAccountId.value = matchingAccount.id;
        }
        messageType.value = statementPreview.value?.ready ? 'success' : 'error';
        message.value = statementPreview.value?.ready ? `已识别 ${providerName(statementPreview.value.provider)} 对账单，完整性校验通过` : '对账单识别完成，但完整性校验未通过';
    } catch (ex: any) {
        messageType.value = 'error';
        message.value = requestError(ex, 'PDF 对账单识别失败');
    } finally {
        previewingStatement.value = false;
    }
}
async function confirmStatement(): Promise<void> {
    const payload = statementFormData();
    if (!payload || !statementPreview.value?.ready || !reconciliationAccountId.value || !confirmDialog.value) return;
    const account = accountName(reconciliationAccountId.value);
    try {
        await confirmDialog.value.open('确认投资对账', `将用 ${providerName(statementPreview.value.provider)}对账单替换“${account}”在 ${statementPreview.value.period_start} 至 ${statementPreview.value.period_end} 内的全部投资交易，并按账单修正持仓。是否继续？`, { color: 'warning' });
    } catch {
        return;
    }
    payload.append('investment_account_id', String(reconciliationAccountId.value));
    payload.append('replace_period', 'true');
    confirmingStatement.value = true;
    message.value = '';
    try {
        const response = await axios.post('v1/hengcai/investments/reconciliation/confirm.json', payload);
        await load();
        await refreshGlobalAccounts();
        messageType.value = 'success';
        message.value = response.data.result?.message || '投资对账已完成';
    } catch (ex: any) {
        messageType.value = 'error';
        message.value = requestError(ex, '投资对账确认失败');
    } finally {
        confirmingStatement.value = false;
    }
}
function maskBrokerAccount(value: string): string {
    if (!value) return '—';
    const compact = value.replace(/\s/g, '');
    return compact.length <= 4 ? compact : `${compact.slice(0, 2)}••••${compact.slice(-4)}`;
}
async function save(kind: 'account' | 'instrument' | 'transaction', payload: any): Promise<any | null> {
    savingKind.value = kind;
    message.value = '';
    try {
        const response = await axios.post('v1/hengcai/investments/save.json', { kind, [kind]: payload });
        await load();
        await refreshGlobalAccounts();
        messageType.value = 'success';
        message.value = kind === 'account' ? '投资账户已保存' : kind === 'instrument' ? '投资标的已保存' : '投资交易已保存，持仓已重新计算';
        return response.data.result;
    } catch (ex: any) {
        messageType.value = 'error';
        message.value = requestError(ex, '投资数据保存失败，请检查输入');
        return null;
    } finally {
        savingKind.value = '';
    }
}
async function addAccount(): Promise<void> {
    const saved = await save('account', accountForm.value);
    if (!saved) return;
    transactionForm.value.investment_account_id = saved.id;
    accountForm.value = newAccountForm();
    entryTab.value = data.value.instruments.length ? 'transaction' : 'instrument';
}
async function addInstrument(): Promise<void> {
    const saved = await save('instrument', instrumentForm.value);
    if (!saved) return;
    transactionForm.value.instrument_id = saved.id;
    instrumentForm.value = newInstrumentForm();
    entryTab.value = data.value.accounts.length ? 'transaction' : 'account';
}
async function addTransaction(): Promise<void> {
    if (!transactionReady.value) { messageType.value = 'error'; message.value = '请选择账户和标的，并填写大于 0 的数量和成交价'; return; }
    const saved = await save('transaction', transactionForm.value);
    if (saved) Object.assign(transactionForm.value, { quantity: 0, price: 0, fees_minor: 0, taxes_minor: 0 });
}
async function removeInvestment(kind: 'account' | 'instrument' | 'transaction', item: any): Promise<void> {
    if (!confirmDialog.value) return;
    const displayName = kind === 'account' ? item.name : kind === 'instrument' ? `${item.symbol} · ${item.name}` : `${item.action === 'BUY' ? '买入' : '卖出'} ${instrumentName(item.instrument_id)} × ${item.quantity}`;
    const kindName = kind === 'account' ? '投资账户' : kind === 'instrument' ? '投资标的' : '投资交易';
    try {
        const dependencyWarning = kind === 'transaction' ? '删除后持仓和收益将立即重新计算。' : '如果已有关联交易，系统会阻止删除。';
        await confirmDialog.value.open('确认删除', `确定删除${kindName}“${displayName}”吗？${dependencyWarning}`, { color: 'error' });
    } catch {
        return;
    }
    deletingKey.value = `${kind}:${item.id}`;
    message.value = '';
    try {
        await axios.post('v1/hengcai/investments/delete.json', { kind, id: item.id });
        await load();
        await refreshGlobalAccounts();
        messageType.value = 'success';
        message.value = `${kindName}“${displayName}”已删除`;
    } catch (ex: any) {
        messageType.value = 'error';
        message.value = requestError(ex, '删除失败');
    } finally {
        deletingKey.value = '';
    }
}
async function syncPrices(): Promise<void> {
    if (!data.value.instruments.length) return;
    syncing.value = true;
    message.value = '';
    try {
		const response = await axios.post(`v1/hengcai/market/sync.json?mode=latest&feed=${encodeURIComponent(feed.value)}`);
        await load();
        await refreshGlobalAccounts();
		const result = response.data.result || {};
		const failures = result.errors?.length || 0;
		messageType.value = failures ? 'warning' : 'success';
		const successMessage = result.message || `已同步 ${result.prices?.length || 0} 个标的`;
		message.value = failures ? `${successMessage}；${result.errors.slice(0, 3).join('；')}${failures > 3 ? `；另有 ${failures - 3} 项未同步` : ''}` : successMessage;
    } catch (ex: any) {
        messageType.value = 'error';
		message.value = requestError(ex, '当前价格同步失败，请检查 Alpaca 只读配置');
    } finally {
        syncing.value = false;
    }
}
onMounted(() => { load().catch(() => { messageType.value = 'error'; message.value = '投资数据加载失败'; }); });
</script>

<style scoped>
.investment-header { display: flex; align-items: center; justify-content: space-between; gap: 24px; padding-bottom: 16px; }
.market-actions { display: flex; align-items: center; gap: 12px; flex-shrink: 0; }
.feed-select { width: 150px; }
.entry-card { overflow: hidden; }
.entry-form { padding: 32px 24px 20px; }
.record-actions { display: flex; align-items: center; gap: 8px; }
.account-link-controls { display: flex; align-items: center; justify-content: flex-end; gap: 8px; flex-wrap: wrap; }
.account-link-select { width: 290px; }
.summary-card { height: 100%; padding: 20px; }
.summary-value { font-size: 1.65rem; font-weight: 600; line-height: 1.2; }
@media (max-width: 900px) {
    .investment-header { align-items: stretch; flex-direction: column; }
    .market-actions { width: 100%; }
    .feed-select { flex: 1; width: auto; }
    .account-link-controls { align-items: stretch; flex-direction: column; width: 100%; }
    .account-link-select { width: 100%; }
}
</style>
