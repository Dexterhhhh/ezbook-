<template>
    <div>
        <v-card class="mb-4">
            <v-card-title class="d-flex flex-wrap align-center ga-3">
                <span>对账</span>
                <v-spacer/>
                <v-select
                    v-model="month"
                    class="month-selector"
                    :items="monthItems"
                    item-title="title"
                    item-value="value"
                    label="对账月份"
                    density="compact"
                    hide-details
                    @update:model-value="changeMonth"
                />
                <v-chip size="small" :color="dashboard.month_close?.status === 'CLOSED' ? 'success' : 'warning'">{{ dashboard.month_close?.status === 'CLOSED' ? '已月结' : '待对账' }}</v-chip>
            </v-card-title>
            <v-card-subtitle>月中跟踪手工记账进度；月末上传支付宝、微信和银行账单，去重、分类并确认入账</v-card-subtitle>
            <v-card-text>
                <v-alert v-if="message" :type="messageType" variant="tonal" class="mb-4" closable @click:close="message = ''">{{ message }}</v-alert>
                <v-row>
                    <v-col cols="12" sm="6" md="3"><v-card variant="tonal"><v-card-text><div class="text-medium-emphasis">主账本本月支出</div><div class="text-h5">{{ money(dashboard.core_expense_minor) }}</div></v-card-text></v-card></v-col>
                    <v-col cols="12" sm="6" md="3"><v-card variant="tonal"><v-card-text><div class="text-medium-emphasis">主账本本月收入</div><div class="text-h5">{{ money(dashboard.core_income_minor) }}</div></v-card-text></v-card></v-col>
                    <v-col cols="12" sm="6" md="3"><v-card variant="tonal"><v-card-text><div class="text-medium-emphasis">已导入账单</div><div class="text-h5">{{ dashboard.statements.length }} 份</div></v-card-text></v-card></v-col>
                    <v-col cols="12" sm="6" md="3"><v-card variant="tonal"><v-card-text><div class="text-medium-emphasis">待处理流水</div><div class="text-h5">{{ pendingCount }} 笔</div></v-card-text></v-card></v-col>
                </v-row>
            </v-card-text>
        </v-card>

        <v-card>
            <v-tabs v-model="tab" color="primary" grow>
                <v-tab value="progress">本月进度</v-tab><v-tab value="upload">上传账单</v-tab><v-tab value="workspace">对账工作台</v-tab><v-tab value="history">月结历史</v-tab>
            </v-tabs>
            <v-window v-model="tab">
                <v-window-item value="progress"><v-card-text>
                    <v-alert type="info" variant="tonal" class="mb-4">手工记账仍在原“交易详情”中进行；此处只读汇总当月进度，不会产生第二份数据。</v-alert>
                    <v-list><v-list-item title="已记录交易" :subtitle="`${dashboard.core_transaction_count || 0} 笔`"/><v-list-item title="已自动匹配" :subtitle="`${matchedCount} 笔（保留原手工交易 ID）`"/><v-list-item title="待渠道账单覆盖" :subtitle="`${provisionalMerchantCount} 笔`"/><v-list-item title="待信用卡/银行覆盖" :subtitle="`${provisionalFundingCount} 笔`"/><v-list-item title="银行结算证据" :subtitle="`${dashboard.status_counts.EVIDENCE || 0} 笔（不重复入账）`"/></v-list>
                    <div class="mb-4" v-if="dashboard.coverages?.length"><div class="text-subtitle-2 mb-2">滚动覆盖水位</div><v-chip class="me-2 mb-2" v-for="item in dashboard.coverages" :key="item.id || item.Id" :color="(item.status || item.Status) === 'VERIFIED' ? 'success' : 'warning'">{{ providerLabel(item.source || item.Source) }} · {{ dimensionLabel(item.dimension || item.Dimension) }} → {{ item.covered_until || item.CoveredUntil }}</v-chip></div>
                    <v-btn color="primary" to="/transaction/list?pageType=0&dateType=7">继续手工记账</v-btn>
                </v-card-text></v-window-item>

                <v-window-item value="upload"><v-card-text>
                    <v-alert type="info" variant="tonal" class="mb-4">请手动选择识别引擎。支付宝/微信使用官方导出 CSV；招商银行信用卡和储蓄卡使用 PDF。同一文件的 SHA-256 不允许重复导入；储蓄卡普通个人对手方必须逐笔人工确认，“快捷支付”和“快捷退款”会按支付宝/微信结算痕迹自动剔除，“银联快捷支付”暂不自动剔除。</v-alert>
                    <v-form @submit.prevent="upload"><v-row>
                        <v-col cols="12" md="3"><v-select v-model="uploadForm.provider" label="识别引擎" :items="providerItems"/></v-col>
                        <v-col cols="12" md="4"><v-select v-model="uploadForm.account_id" label="对应主账本账户" :items="accountItems"/></v-col>
                        <v-col cols="12" md="5"><v-file-input v-model="files" label="账单文件" :accept="['CMB', 'CMB_SAVINGS'].includes(uploadForm.provider) ? '.pdf,application/pdf' : '.csv,text/csv'" clearable/></v-col>
                        <v-col cols="12"><v-btn type="submit" color="primary" :loading="uploading" :disabled="!selectedFile || !uploadForm.account_id">上传、解析并自动匹配</v-btn></v-col>
                    </v-row></v-form>
                </v-card-text></v-window-item>

                <v-window-item value="workspace"><v-card-text>
                    <div class="d-flex flex-wrap ga-2 mb-4"><v-chip v-for="(count, status) in dashboard.status_counts" :key="status">{{ statusLabel(String(status)) }} {{ count }}</v-chip><v-spacer/><v-btn variant="tonal" :loading="classifying" :disabled="classifying || !dashboard.statements.length" @click="classifyAll">{{ classifying ? '分类建议生成中（AI 处理约需 1–3 分钟）' : '生成分类建议' }}</v-btn></div>
                    <v-card variant="tonal" class="mb-4"><v-card-title class="text-subtitle-1 d-flex align-center"><span>流水筛选</span><v-spacer/><v-btn size="small" variant="text" :disabled="!hasActiveFilters" @click="clearFilters">清空筛选</v-btn></v-card-title><v-card-text><v-row dense>
                        <v-col cols="12" sm="6" lg="3"><v-select v-model="filters.provider" :items="providerFilterItems" label="来源" density="compact" hide-details clearable/></v-col>
                        <v-col cols="12" sm="6" lg="3"><v-select v-model="filters.merchantChannel" :items="merchantChannelFilterItems" label="消费渠道" density="compact" hide-details clearable/></v-col>
                        <v-col cols="12" sm="6" lg="3"><v-select v-model="filters.fundingSource" :items="fundingSourceFilterItems" label="资金来源" density="compact" hide-details clearable/></v-col>
                        <v-col cols="12" sm="6" lg="3"><v-select v-model="filters.statementCategory" :items="statementCategoryFilterItems" label="账单分类" density="compact" hide-details clearable/></v-col>
                        <v-col cols="12" sm="6" lg="3"><v-text-field v-model="filters.keyword" label="商户 / 摘要" density="compact" hide-details clearable/></v-col>
                        <v-col cols="12" sm="6" lg="3"><v-select v-model="filters.kind" :items="kindFilterItems" label="类型" density="compact" hide-details clearable/></v-col>
                        <v-col cols="6" sm="3" lg="2"><v-text-field v-model="filters.minAmountYuan" type="number" step="0.01" label="最小金额（元）" density="compact" hide-details clearable/></v-col>
                        <v-col cols="6" sm="3" lg="2"><v-text-field v-model="filters.maxAmountYuan" type="number" step="0.01" label="最大金额（元）" density="compact" hide-details clearable/></v-col>
                        <v-col cols="12" sm="6" lg="3"><v-select v-model="filters.status" :items="statusFilterItems" label="处理状态" density="compact" hide-details clearable/></v-col>
                        <v-col cols="12" sm="6" lg="3"><v-select v-model="filters.action" :items="actionFilterItems" label="处理方式" density="compact" hide-details clearable/></v-col>
                    </v-row></v-card-text></v-card>
                    <div class="table-wrap"><v-table density="compact"><thead><tr><th>日期</th><th>来源</th><th>消费渠道</th><th>资金来源</th><th>账单分类</th><th>商户/摘要</th><th><span class="sortable-header">类型<v-btn icon size="x-small" variant="text" :aria-label="sortAriaLabel('kind', '类型')" @click="toggleSort('kind')"><span class="sort-arrow">{{ sortIcon('kind') }}</span></v-btn></span></th><th class="text-end"><span class="sortable-header justify-end">金额<v-btn icon size="x-small" variant="text" :aria-label="sortAriaLabel('amount', '金额')" @click="toggleSort('amount')"><span class="sort-arrow">{{ sortIcon('amount') }}</span></v-btn></span></th><th><span class="sortable-header">处理状态<v-btn icon size="x-small" variant="text" :aria-label="sortAriaLabel('status', '处理状态')" @click="toggleSort('status')"><span class="sort-arrow">{{ sortIcon('status') }}</span></v-btn></span></th><th>处理方式</th></tr></thead><tbody>
                        <tr v-if="!dashboard.lines.length"><td colspan="10" class="text-center py-8">本月尚未上传账单</td></tr>
						<tr v-else-if="!filteredLines.length"><td colspan="10" class="text-center py-8">没有符合当前筛选条件的流水</td></tr>
						<tr v-for="line in paginatedLines" :key="line.Id || line.id"><td>{{ line.PostedDate || line.posted_date }}</td><td>{{ statementProvider(line.StatementId || line.statement_id) }}</td><td>{{ channelLabel(line.MerchantChannel || line.merchant_channel) }}</td><td>{{ fundingLabel(line.FundingSource || line.funding_source) }}</td><td>{{ line.StatementCategory || line.statement_category || '—' }}</td><td class="description-cell"><div>{{ description(line) }}</div><div v-if="counterparty(line)" class="text-caption text-medium-emphasis">对手方：{{ counterparty(line) }}</div><v-chip v-if="counterpartyType(line) === 'PERSON' && requiresManual(line) && !isExcludedSettlement(line)" size="x-small" color="warning" variant="tonal">{{ isUnionPayShortcut(line) ? '银联快捷支付 · 暂待核查' : '个人对手方 · 必须手工' }}</v-chip></td><td>{{ line.LineKind || line.line_kind }}</td><td class="text-end" :class="Number(line.SignedAmountMinor ?? line.signed_amount_minor ?? line.AmountMinor ?? line.amount_minor) < 0 ? 'text-success' : ''">{{ money(line.SignedAmountMinor ?? line.signed_amount_minor ?? line.AmountMinor ?? line.amount_minor) }}</td><td><v-chip size="small" :color="statusColor(line.Status || line.status)">{{ statusLabel(line.Status || line.status) }}</v-chip></td><td style="min-width:240px">
                            <v-chip v-if="lineStatus(line) === 'POSTED'" size="small" color="success" variant="tonal">已入账 · 反入账后可修改</v-chip>
                            <v-chip v-else-if="isExcludedSettlement(line)" size="small" color="info" variant="tonal">支付宝/微信已覆盖 · 银行流水剔除</v-chip>
							<div v-else-if="isPlatformUnresolved(line)" class="d-flex align-center ga-2 flex-wrap"><v-select :items="categoryItemsForLine(line)" :model-value="line.category_id || null" density="compact" hide-details clearable placeholder="先选择分类" @update:model-value="saveCategory(line, $event)"/><v-btn size="small" color="primary" variant="tonal" :disabled="!Number(line.category_id || 0)" @click="resolvePlatformLine(line, 'CONFIRM_PLATFORM_LEDGER')">确认独立入账</v-btn><v-btn size="small" color="info" variant="tonal" @click="resolvePlatformLine(line, 'CONFIRM_PLATFORM_EVIDENCE')">平台已覆盖，不入账</v-btn></div>
							<div v-else-if="counterpartyType(line) === 'PERSON' && requiresManual(line)" class="d-flex align-center ga-2 flex-wrap"><v-select v-if="['TRANSFER', 'REPAYMENT'].includes(lineKind(line))" :items="manualActionItems" label="选择处理方式" density="compact" hide-details @update:model-value="chooseManualAction(line, $event)"/><v-btn v-else size="small" color="warning" variant="tonal" @click="openManualDialog(line)">手工处理</v-btn><v-btn v-if="lineKind(line) === 'INCOME'" size="small" color="secondary" variant="tonal" @click="openRefundDialog(line)">作为退款</v-btn><v-select v-if="['PURCHASE', 'REFUND', 'INCOME'].includes(lineKind(line))" :items="categoryItemsForLine(line)" :model-value="line.category_id || null" density="compact" hide-details clearable placeholder="未分类" @update:model-value="saveCategory(line, $event)"/></div>
                            <div v-else-if="lineKind(line) === 'INSTALLMENT_PRINCIPAL'" class="d-flex align-center ga-2"><v-select :items="capexInstallmentItemsForLine(line)" :model-value="capexInstallmentForLine(line)" density="compact" hide-details :placeholder="capexInstallmentItemsForLine(line).length ? '选择当前期次' : '没有匹配计划'" @update:model-value="linkCapex(line, $event)"/><v-btn v-if="!capexInstallmentForLine(line) && recommendedCapexForLine(line)" size="small" color="primary" variant="tonal" @click="linkCapex(line, recommendedCapexForLine(line))">关联推荐</v-btn><v-btn size="small" variant="tonal" @click="openCapexDialog(line)">新增</v-btn></div>
                            <v-chip v-else-if="['REPAYMENT', 'INSTALLMENT_SETUP'].includes(lineKind(line))" size="small" color="info" variant="tonal">{{ lineKind(line) === 'REPAYMENT' ? '信用卡还款证据' : '分期计划调整证据' }}</v-chip>
                            <div v-else-if="lineKind(line) === 'INCOME'" class="d-flex align-center ga-2 flex-wrap"><v-btn size="small" color="secondary" variant="tonal" @click="openRefundDialog(line)">作为退款</v-btn><v-select :items="categoryItemsForLine(line)" :model-value="line.category_id || null" density="compact" hide-details clearable placeholder="收入分类" @update:model-value="saveCategory(line, $event)"/></div>
                            <v-select v-else :items="categoryItemsForLine(line)" :model-value="line.category_id || null" density="compact" hide-details clearable placeholder="未分类" @update:model-value="saveCategory(line, $event)"/>
                        </td></tr>
					</tbody></v-table></div>
					<div class="d-flex flex-wrap align-center justify-end ga-3 mt-3"><v-select v-model="pageSize" :items="pageSizeItems" label="每页显示" density="compact" hide-details style="max-width:150px" @update:model-value="currentPage = 1"/><span class="text-body-2 text-medium-emphasis">已显示 {{ filteredLines.length }} / {{ dashboard.lines.length }} 条</span><v-pagination v-if="pageCount > 1" v-model="currentPage" :length="pageCount" :total-visible="7" density="compact"/></div>
                    <v-alert type="warning" variant="tonal" class="my-4">AI 只提供分类建议。对账状态、去重和入账始终由确定性规则与人工确认决定。</v-alert>
                    <div class="d-flex flex-wrap ga-3"><v-btn color="primary" :loading="posting" :disabled="pendingCount > 0 || !dashboard.statements.length || dashboard.month_close?.status === 'CLOSED'" @click="postAll">确认并入账</v-btn><v-btn color="success" variant="tonal" :disabled="pendingCount > 0 || unpostedStatementCount > 0 || !dashboard.statements.length || dashboard.month_close?.status === 'CLOSED'" @click="closeMonth">完成月结</v-btn><v-btn v-if="dashboard.month_close?.status === 'CLOSED'" color="warning" variant="tonal" @click="reopen">重新打开本月</v-btn></div>
                </v-card-text></v-window-item>

                <v-window-item value="history"><v-card-text><v-alert type="info" variant="tonal" class="mb-4">自然月账单覆盖“消费渠道”；信用卡非自然月账单覆盖“资金来源”。未完成月结时，已入账账单可反入账后修改凭据；反入账会保留审计记录。</v-alert><v-table><thead><tr><th>导入时间</th><th>来源</th><th>覆盖维度</th><th>周期类型</th><th>文件</th><th>期间</th><th>覆盖状态</th><th>状态</th><th>操作</th></tr></thead><tbody><tr v-if="!dashboard.statements.length"><td colspan="9" class="text-center py-8">本月无导入历史</td></tr><tr v-for="item in dashboard.statements" :key="item.Id || item.id"><td>{{ formatTime(item.CreatedUnixTime || item.created_unix_time) }}</td><td>{{ providerLabel(item.Provider || item.provider) }}</td><td>{{ dimensionLabel(item.CoverageDimension || item.coverage_dimension) }}</td><td>{{ periodTypeLabel(item.PeriodType || item.period_type) }}</td><td>{{ item.FileName || item.file_name || '旧版导入' }}</td><td>{{ item.PeriodStart || item.period_start }} ~ {{ item.PeriodEnd || item.period_end }}</td><td>{{ coverageStatusLabel(item.CoverageStatus || item.coverage_status) }}</td><td>{{ item.Status || item.status }}</td><td><v-btn v-if="statementStatus(item) === 'POSTED' && dashboard.month_close?.status !== 'CLOSED'" size="small" color="warning" variant="tonal" @click="openUnpostDialog(item)">反入账</v-btn><span v-else class="text-medium-emphasis">—</span></td></tr></tbody></v-table></v-card-text></v-window-item>
            </v-window>
        </v-card>

        <v-dialog v-model="manualDialog" max-width="720"><v-card><v-card-title>人工处理个人对手方</v-card-title><v-card-subtitle>系统不会替个人对手方自动匹配或入账，请选择已有主账本交易；转账/还款也可以确认不进入主账本。</v-card-subtitle><v-card-text><v-alert v-if="manualLine" type="warning" variant="tonal" class="mb-4">{{ manualLine.PostedDate || manualLine.posted_date }} · {{ counterparty(manualLine) }} · {{ money(lineSignedAmount(manualLine)) }}</v-alert><v-select v-model="manualCandidateId" :items="manualCandidateItems" label="匹配已有主账本交易" clearable :loading="manualBusy" no-data-text="没有找到同账户、同金额且日期相差不超过 7 天的交易"/><v-alert v-if="manualCandidates.length === 0 && !manualBusy" type="info" variant="tonal">没有可直接匹配的交易。若这是转账或还款，可点击“确认不入主账本”；消费或收入请先在下拉框选择分类。</v-alert></v-card-text><v-card-actions><v-spacer/><v-btn @click="manualDialog=false">取消</v-btn><v-btn v-if="manualLine && ['TRANSFER', 'REPAYMENT'].includes(lineKind(manualLine))" color="secondary" variant="tonal" :loading="manualBusy" @click="confirmManualReview">确认不入主账本</v-btn><v-btn color="primary" :disabled="!manualCandidateId" :loading="manualBusy" @click="confirmManualMatch">匹配已有交易</v-btn></v-card-actions></v-card></v-dialog>
        <v-dialog v-model="refundDialog" max-width="640"><v-card><v-card-title>将收入作为退款</v-card-title><v-card-subtitle>这笔银行入账不会计入收入，而会按负支出冲减原支出。请选择原支出的二级分类。</v-card-subtitle><v-card-text><v-alert v-if="refundLine" type="warning" variant="tonal" class="mb-4">{{ refundLine.PostedDate || refundLine.posted_date }} · {{ description(refundLine) }} · {{ money(lineSignedAmount(refundLine)) }}</v-alert><v-alert type="info" variant="tonal" class="mb-4">仅在你确认它是退款、且不应与支付宝/微信中的同一笔退款重复入账时使用。银联快捷支付仍需人工核查。</v-alert><v-select v-model="refundCategoryId" :items="expenseCategoryItems" label="原支出分类" :loading="refundBusy" clearable placeholder="请选择支出分类"/></v-card-text><v-card-actions><v-spacer/><v-btn @click="refundDialog=false">取消</v-btn><v-btn color="primary" :disabled="!refundCategoryId" :loading="refundBusy" @click="confirmRefund">确认按退款入账</v-btn></v-card-actions></v-card></v-dialog>
        <v-dialog v-model="unpostDialog" max-width="640"><v-card><v-card-title>反入账账单</v-card-title><v-card-subtitle>撤销本账单的入账结果，恢复凭据编辑后可重新确认入账</v-card-subtitle><v-card-text><v-alert v-if="unpostStatement" type="warning" variant="tonal" class="mb-4">{{ providerLabel(unpostStatement.Provider || unpostStatement.provider) }} · {{ unpostStatement.FileName || unpostStatement.file_name || '旧版导入' }} · {{ unpostStatement.PeriodStart || unpostStatement.period_start }} ~ {{ unpostStatement.PeriodEnd || unpostStatement.period_end }}</v-alert><v-alert type="info" variant="tonal" class="mb-4">由本账单创建的交易和账户余额会同步回退；匹配到既有手工交易的凭据只解除关联，不会删除原交易。CAPEX 本金核销也会同步回退。</v-alert><v-textarea v-model="unpostReason" label="反入账原因" rows="2" maxlength="255" counter/></v-card-text><v-card-actions><v-spacer/><v-btn :disabled="unposting" @click="unpostDialog=false">取消</v-btn><v-btn color="warning" :loading="unposting" :disabled="!unpostReason.trim()" @click="confirmUnpost">确认反入账</v-btn></v-card-actions></v-card></v-dialog>

        <v-dialog v-model="capexDialog" max-width="720"><v-card><v-card-title>新增 CAPEX 并关联本期本金</v-card-title><v-card-subtitle>系统已根据账单估算金额和期次，请确认后保存</v-card-subtitle><v-card-text><v-row>
            <v-col cols="12" md="6"><v-text-field v-model="capexForm.item_name" label="项目名称"/></v-col><v-col cols="12" md="3"><v-text-field v-model="capexForm.purchase_date" label="购买日期"/></v-col><v-col cols="12" md="3"><v-text-field v-model.number="capexForm.total_amount_yuan" type="number" min="0.01" step="0.01" label="分期本金总额（元）" hint="本期本金 × 总期数，不包含首付款" persistent-hint/></v-col>
            <v-col cols="12" md="3"><v-text-field v-model.number="capexForm.down_payment_yuan" type="number" min="0" step="0.01" label="购买时首付款（元）" hint="不是本期分期本金；没有首付款请填 0" persistent-hint/></v-col><v-col cols="12" md="3"><v-text-field v-model.number="capexForm.installment_count" type="number" min="1" step="1" label="总期数"/></v-col><v-col cols="12" md="3"><v-text-field v-model.number="capexTargetNo" type="number" min="1" step="1" label="当前期次"/></v-col><v-col cols="12" md="3"><v-text-field v-model="capexForm.first_due_date" label="首期日期"/></v-col>
        </v-row><v-alert v-if="capexDialogMessage || !capexFormValid" type="error" variant="tonal" class="mb-3">{{ capexDialogMessage || '请检查金额、日期和期次；金额按元填写，最多两位小数' }}</v-alert><v-alert type="info" variant="tonal">保存的 CAPEX 总额为 {{ money(capexTotalAmountYuan * 100) }}（分期本金总额 + 购买时首付款）；分期利息单独计入“利息支出”。</v-alert></v-card-text><v-card-actions><v-spacer/><v-btn @click="capexDialog=false">取消</v-btn><v-btn color="primary" :loading="savingCapex" :disabled="!capexFormValid" @click="createAndLinkCapex">创建并关联</v-btn></v-card-actions></v-card></v-dialog>
    </div>
</template>

<script setup lang="ts">
import axios from 'axios';
import { computed, onMounted, ref, watch } from 'vue';
import { DEFAULT_LLM_API_TIMEOUT } from '@/consts/api';

const tab = ref('progress');
const today = new Date();
const currentMonthIndex = today.getFullYear() * 12 + today.getMonth();
const monthItems = Array.from({ length: 73 }, (_, index) => {
    const absoluteMonth = currentMonthIndex + 12 - index;
    const year = Math.floor(absoluteMonth / 12);
    const monthNumber = absoluteMonth % 12 + 1;
    return { title: `${year} 年 ${monthNumber} 月`, value: `${year}-${String(monthNumber).padStart(2, '0')}` };
});
const month = ref(`${today.getFullYear()}-${String(today.getMonth() + 1).padStart(2, '0')}`);
const dashboard = ref<any>({ statements: [], lines: [], status_counts: {}, manual_markers: [], coverages: [], categories: [], capex_purchases: [], capex_installments: [], capex_settlements: [] });
const currentPage = ref(1); const pageSize = ref(10); const pageSizeItems = [{ title: '10 条', value: 10 }, { title: '20 条', value: 20 }, { title: '50 条', value: 50 }, { title: '100 条', value: 100 }, { title: '全部', value: -1 }];
const message = ref(''); const messageType = ref<'success'|'error'|'info'>('info'); const uploading = ref(false); const classifying = ref(false); const posting = ref(false);
const manualDialog = ref(false); const manualBusy = ref(false); const manualLine = ref<any>(null); const manualCandidates = ref<any[]>([]); const manualCandidateId = ref<string|null>(null);
const refundDialog = ref(false); const refundBusy = ref(false); const refundLine = ref<any>(null); const refundCategoryId = ref<string|null>(null);
const unpostDialog = ref(false); const unposting = ref(false); const unpostStatement = ref<any>(null); const unpostReason = ref('修改账单凭据');
const capexDialog = ref(false); const savingCapex = ref(false); const capexDialogMessage = ref(''); const capexTargetLine = ref<any>(null); const capexTargetNo = ref(1);
const capexForm = ref({ item_name: '', purchase_date: '', total_amount_yuan: 0, down_payment_yuan: 0, installment_count: 1, first_due_date: '', financing_type: 'INSTALLMENT', currency: 'CNY', note: '由信用卡账单分期本金创建' });
const capexTotalAmountYuan = computed(() => Number(capexForm.value.total_amount_yuan || 0) + Number(capexForm.value.down_payment_yuan || 0));
const capexFormValid = computed(() => Boolean(capexForm.value.item_name.trim()) && /^\d{4}-\d{2}-\d{2}$/.test(capexForm.value.purchase_date) && /^\d{4}-\d{2}-\d{2}$/.test(capexForm.value.first_due_date) && Number(capexForm.value.total_amount_yuan) > 0 && Number(capexForm.value.down_payment_yuan) >= 0 && Number.isInteger(Number(capexForm.value.installment_count)) && Number(capexForm.value.installment_count) >= 1 && Number.isInteger(Number(capexTargetNo.value)) && Number(capexTargetNo.value) >= 1 && Number(capexTargetNo.value) <= Number(capexForm.value.installment_count));
const uploadForm = ref({ provider: 'ALIPAY', account_id: '' }); const files = ref<File[]|File|null>(null); const coreAccounts = ref<any[]>([]);
const providerItems = [{ title: '支付宝 CSV', value: 'ALIPAY' }, { title: '微信支付 CSV', value: 'WECHAT' }, { title: '招商银行信用卡 PDF', value: 'CMB' }, { title: '招商银行储蓄卡 PDF', value: 'CMB_SAVINGS' }];
const selectedFile = computed(() => Array.isArray(files.value) ? files.value[0] || null : files.value);
const flattenAccounts = (items: any[]): any[] => items.flatMap(item => [item, ...flattenAccounts(item.subAccounts || [])]);
const accountItems = computed(() => coreAccounts.value.map(item => ({ title: `${item.name} · ${item.currency}`, value: item.id })));
const categoryItems = computed(() => (dashboard.value.categories || []).map((c: any) => ({ title: `${c.name}${Number(c.type) === 1 ? '（收入）' : '（支出）'}`, value: c.category_id })));
const expenseCategoryItems = computed(() => categoryItems.value.filter((item: any) => item.title.endsWith('（支出）')));
const lineKind = (line: any): string => line.LineKind || line.line_kind || '';
const lineStatus = (line: any): string => line.Status || line.status || '';
const lineId = (line: any): number => Number(line.Id || line.id);
const matchType = (line: any): string => line.MatchType || line.match_type || '';
const description = (line: any): string => String(line.Description || line.description || '').trim();
const counterparty = (line: any): string => line.Counterparty || line.counterparty || '';
const counterpartyType = (line: any): string => line.CounterpartyType || line.counterparty_type || '';
const requiresManual = (line: any): boolean => Boolean(line.RequiresReview ?? line.requires_review);
const isExcludedSettlement = (line: any): boolean => matchType(line) === 'PLATFORM_SETTLEMENT_EXCLUDED';
const isPlatformUnresolved = (line: any): boolean => lineStatus(line) === 'REVIEW' && matchType(line) === 'PLATFORM_UNRESOLVED';
const isUnionPayShortcut = (line: any): boolean => description(line) === '银联快捷支付';
type SortKey = 'kind'|'amount'|'status';
const sortState = ref<{ key: SortKey|''; direction: 'asc'|'desc'|'' }>({ key: '', direction: '' });
const statusSortRank: Record<string, number> = { UNMATCHED: 0, REVIEW: 1, CLASSIFIED: 2, CAPEX_LINKED: 3, MATCHED: 4, EVIDENCE: 5, POSTED: 6 };
const lineSignedAmount = (line: any): number => Number(line.SignedAmountMinor ?? line.signed_amount_minor ?? line.AmountMinor ?? line.amount_minor ?? 0);
const statementId = (statement: any): number => Number(statement?.Id || statement?.id || 0);
const statementStatus = (statement: any): string => statement?.Status || statement?.status || '';
const statementForLine = (line: any): any => dashboard.value.statements.find((item: any) => statementId(item) === Number(line.StatementId || line.statement_id));
const statementProviderRaw = (line: any): string => statementForLine(line)?.Provider || statementForLine(line)?.provider || '';
const lineMerchantChannel = (line: any): string => line.MerchantChannel || line.merchant_channel || '';
const lineFundingSource = (line: any): string => line.FundingSource || line.funding_source || '';
const lineStatementCategory = (line: any): string => line.StatementCategory || line.statement_category || '';
const filters = ref({ provider: '', merchantChannel: '', fundingSource: '', statementCategory: '', keyword: '', kind: '', minAmountYuan: '', maxAmountYuan: '', status: '', action: '' });
const emptyFilterValue = '__EMPTY__';
const uniqueFilterItems = (values: string[], label: (value: string) => string = value => value, emptyLabel = '未设置'): any[] => {
    const items = [...new Set(values.filter(Boolean))].sort((a, b) => label(a).localeCompare(label(b), 'zh-CN')).map(value => ({ title: label(value), value }));
    if (values.some(value => !value)) items.unshift({ title: emptyLabel, value: emptyFilterValue });
    return items;
};
const filterMatches = (selected: string, actual: string): boolean => !selected || (selected === emptyFilterValue ? !actual : actual === selected);
const providerFilterItems = computed(() => uniqueFilterItems((dashboard.value.lines || []).map(statementProviderRaw), providerLabel, '旧版/未识别来源'));
const merchantChannelFilterItems = computed(() => uniqueFilterItems((dashboard.value.lines || []).map(lineMerchantChannel), channelLabel, '未识别消费渠道'));
const fundingSourceFilterItems = computed(() => uniqueFilterItems((dashboard.value.lines || []).map(lineFundingSource), fundingLabel, '未识别资金来源'));
const statementCategoryFilterItems = computed(() => uniqueFilterItems((dashboard.value.lines || []).map(lineStatementCategory), value => value, '未设置账单分类'));
const kindFilterItems = computed(() => uniqueFilterItems((dashboard.value.lines || []).map(lineKind), value => value, '未识别类型'));
const statusFilterItems = computed(() => uniqueFilterItems((dashboard.value.lines || []).map(lineStatus), statusLabel, '未设置状态'));
const actionFilterItems = [{ title: '已完成（已入账/凭据）', value: 'COMPLETED' }, { title: '人工确认', value: 'MANUAL' }, { title: 'CAPEX 关联', value: 'CAPEX' }, { title: '还款/计划证据', value: 'EVIDENCE' }, { title: '分类录入', value: 'CLASSIFY' }, { title: '平台覆盖剔除', value: 'EXCLUDED' }];
function lineAction(line: any): string {
    if (isExcludedSettlement(line)) return 'EXCLUDED';
    if (['POSTED', 'EVIDENCE'].includes(lineStatus(line))) return 'COMPLETED';
    if (isPlatformUnresolved(line) || (counterpartyType(line) === 'PERSON' && requiresManual(line))) return 'MANUAL';
    if (lineKind(line) === 'INSTALLMENT_PRINCIPAL') return 'CAPEX';
    if (['REPAYMENT', 'INSTALLMENT_SETUP'].includes(lineKind(line))) return 'EVIDENCE';
    return 'CLASSIFY';
}
const hasActiveFilters = computed(() => Object.values(filters.value).some(value => String(value ?? '').trim() !== ''));
const filteredLines = computed(() => {
    const keyword = filters.value.keyword.trim().toLocaleLowerCase('zh-CN');
    const minAmount = filters.value.minAmountYuan === '' ? null : Number(filters.value.minAmountYuan) * 100;
    const maxAmount = filters.value.maxAmountYuan === '' ? null : Number(filters.value.maxAmountYuan) * 100;
    return (dashboard.value.lines || []).filter((line: any) => {
        const merchantText = `${description(line)} ${counterparty(line)}`.toLocaleLowerCase('zh-CN');
        const amount = lineSignedAmount(line);
        return filterMatches(filters.value.provider, statementProviderRaw(line))
            && filterMatches(filters.value.merchantChannel, lineMerchantChannel(line))
            && filterMatches(filters.value.fundingSource, lineFundingSource(line))
            && filterMatches(filters.value.statementCategory, lineStatementCategory(line))
            && (!keyword || merchantText.includes(keyword))
            && filterMatches(filters.value.kind, lineKind(line))
            && (minAmount === null || !Number.isFinite(minAmount) || amount >= minAmount)
            && (maxAmount === null || !Number.isFinite(maxAmount) || amount <= maxAmount)
            && filterMatches(filters.value.status, lineStatus(line))
            && (!filters.value.action || lineAction(line) === filters.value.action);
    });
});
const sortedLines = computed(() => {
    const indexed = filteredLines.value.map((line: any, index: number) => ({ line, index }));
    const { key, direction } = sortState.value;
    if (!key || !direction) return indexed.map((item: any) => item.line);
    const factor = direction === 'asc' ? 1 : -1;
    indexed.sort((left: any, right: any) => {
        let comparison = 0;
        if (key === 'amount') comparison = lineSignedAmount(left.line) - lineSignedAmount(right.line);
        else if (key === 'status') comparison = (statusSortRank[lineStatus(left.line)] ?? 99) - (statusSortRank[lineStatus(right.line)] ?? 99);
        else comparison = lineKind(left.line).localeCompare(lineKind(right.line), 'zh-CN');
        return comparison === 0 ? left.index - right.index : comparison * factor;
    });
    return indexed.map((item: any) => item.line);
});
const pageCount = computed(() => pageSize.value < 0 ? 1 : Math.max(1, Math.ceil(sortedLines.value.length / pageSize.value)));
const paginatedLines = computed(() => { if (pageSize.value < 0) return sortedLines.value; const safePage = Math.min(currentPage.value, pageCount.value); const start = (safePage - 1) * pageSize.value; return sortedLines.value.slice(start, start + pageSize.value); });
function toggleSort(key: SortKey): void { if (sortState.value.key !== key) sortState.value = { key, direction: 'asc' }; else if (sortState.value.direction === 'asc') sortState.value = { key, direction: 'desc' }; else sortState.value = { key: '', direction: '' }; }
function sortIcon(key: SortKey): string { return sortState.value.key !== key ? '↕' : sortState.value.direction === 'asc' ? '↑' : '↓'; }
function sortAriaLabel(key: SortKey, label: string): string { if (sortState.value.key !== key) return `${label}升序`; if (sortState.value.direction === 'asc') return `${label}降序`; return `取消${label}排序`; }
function clearFilters(): void { filters.value = { provider: '', merchantChannel: '', fundingSource: '', statementCategory: '', keyword: '', kind: '', minAmountYuan: '', maxAmountYuan: '', status: '', action: '' }; }
watch(filters, () => { currentPage.value = 1; }, { deep: true });
const lineNeedsResolution = (line: any): boolean => {
    if (['POSTED', 'EVIDENCE', 'CAPEX_LINKED'].includes(lineStatus(line))) return false;
    if (lineKind(line) === 'INSTALLMENT_PRINCIPAL') return !capexInstallmentForLine(line);
    if (['REPAYMENT', 'INSTALLMENT_SETUP'].includes(lineKind(line))) return true;
    return !Number(line.category_id || 0) || ['UNMATCHED', 'REVIEW'].includes(lineStatus(line));
};
const pendingCount = computed(() => (dashboard.value.lines || []).filter(lineNeedsResolution).length);
const unpostedStatementCount = computed(() => (dashboard.value.statements || []).filter((item: any) => statementStatus(item) !== 'POSTED').length);
const categoryItemsForLine = (line: any): any[] => categoryItems.value.filter((item: any) => lineKind(line) === 'INCOME' ? item.title.endsWith('（收入）') : item.title.endsWith('（支出）'));
const capexPurchase = (purchaseId: number): any => (dashboard.value.capex_purchases || []).find((item: any) => Number(item.id || item.Id) === Number(purchaseId));
const capexPurchaseName = (purchaseId: number): string => capexPurchase(purchaseId)?.item_name || `CAPEX #${purchaseId}`;
const capexInstallmentForLine = (line: any): number|null => { const item = (dashboard.value.capex_settlements || []).find((v: any) => Number(v.statement_line_id || v.StatementLineId) === lineId(line)); return item ? Number(item.installment_id || item.InstallmentId) : null; };
const installmentPhase = (line: any): { current: number; total: number }|null => { const matched = String(line.Description || line.description || '').match(/第\s*(\d+)\s*\/\s*(\d+)\s*期/); if (!matched) return null; const current = Number(matched[1]); const total = Number(matched[2]); return current >= 1 && total >= current ? { current, total } : null; };
const dateDistance = (left: string, right: string): number => { const a = Date.parse(`${left}T00:00:00Z`); const b = Date.parse(`${right}T00:00:00Z`); return Number.isFinite(a) && Number.isFinite(b) ? Math.abs(a - b) / 86400000 : 9999; };
function capexCandidatesForLine(line: any): any[] {
    const phase = installmentPhase(line); if (!phase) return [];
    const installments = dashboard.value.capex_installments || []; const settlements = dashboard.value.capex_settlements || []; const currentLink = capexInstallmentForLine(line); const amount = Math.abs(lineSignedAmount(line)); const postedDate = String(line.PostedDate || line.posted_date || '');
    const occupied = new Set(settlements.filter((item: any) => Boolean(item.posted ?? item.Posted) && Number(item.statement_line_id || item.StatementLineId) !== lineId(line)).map((item: any) => Number(item.installment_id || item.InstallmentId)));
    const candidates = installments.filter((item: any) => { const id = Number(item.id || item.Id); const purchase = capexPurchase(Number(item.purchase_id || item.PurchaseId)); return Number(item.installment_no || item.InstallmentNo) === phase.current && Number(purchase?.installment_count || purchase?.InstallmentCount) === phase.total && (!occupied.has(id) || id === currentLink); }).map((item: any) => {
        const purchaseId = Number(item.purchase_id || item.PurchaseId); const prior = installments.find((candidate: any) => Number(candidate.purchase_id || candidate.PurchaseId) === purchaseId && Number(candidate.installment_no || candidate.InstallmentNo) === phase.current - 1); const priorSettlement = prior && settlements.find((settlement: any) => Number(settlement.installment_id || settlement.InstallmentId) === Number(prior.id || prior.Id) && Boolean(settlement.posted ?? settlement.Posted)); const planned = Number(item.principal_minor ?? item.PrincipalMinor ?? 0); const amountRatio = amount > 0 ? Math.abs(planned - amount) / amount : 1; const days = dateDistance(postedDate, String(item.due_date || item.DueDate || '')); let score = 0; if (priorSettlement) score += 100; if (priorSettlement && Number(priorSettlement.principal_minor ?? priorSettlement.PrincipalMinor) === amount) score += 50; if (days <= 7) score += 35; else if (days <= 31) score += 20; if (planned === amount) score += 30; else if (amountRatio <= 0.15) score += 15; return { item, score, hasPrior: Boolean(priorSettlement), days };
    }).sort((a: any, b: any) => b.score - a.score || a.days - b.days);
    const chained = candidates.filter((candidate: any) => candidate.hasPrior); if (chained.length) return chained;
    const close = candidates.filter((candidate: any) => candidate.days <= 45 || candidate.score >= 15); return close.length ? close : candidates;
}
function capexInstallmentItemsForLine(line: any): any[] { return capexCandidatesForLine(line).map((candidate: any, index: number) => { const item = candidate.item; const purchaseId = Number(item.purchase_id || item.PurchaseId); const prefix = index === 0 && candidate.score >= 100 ? '推荐 · ' : ''; return { title: `${prefix}${capexPurchaseName(purchaseId)} · 第 ${item.installment_no || item.InstallmentNo}/${capexPurchase(purchaseId)?.installment_count || capexPurchase(purchaseId)?.InstallmentCount} 期 · 到期 ${item.due_date || item.DueDate} · 计划 ${money(item.principal_minor ?? item.PrincipalMinor)}`, value: Number(item.id || item.Id) }; }); }
function recommendedCapexForLine(line: any): number|null { const candidate = capexCandidatesForLine(line)[0]; return candidate?.score >= 100 ? Number(candidate.item.id || candidate.item.Id) : null; }
const matchedCount = computed(() => (dashboard.value.status_counts?.MATCHED || 0));
const provisionalMerchantCount = computed(() => (dashboard.value.manual_markers || []).filter((item: any) => (item.merchant_channel || item.MerchantChannel) && (item.merchant_state || item.MerchantState) !== 'VERIFIED').length);
const provisionalFundingCount = computed(() => (dashboard.value.manual_markers || []).filter((item: any) => (item.funding_source || item.FundingSource) && (item.funding_state || item.FundingState) !== 'VERIFIED').length);
const money = (minor: number): string => new Intl.NumberFormat('zh-CN', { style: 'currency', currency: 'CNY' }).format(Number(minor || 0) / 100);
const formatTime = (value: number): string => value ? new Date(value * 1000).toLocaleString('zh-CN') : '—';
const statusLabel = (value: string): string => ({ UNMATCHED: '待匹配', MATCHED: '已匹配手工记录', EVIDENCE: '结算证据', REVIEW: '需人工处理', CLASSIFIED: '已分类', CAPEX_LINKED: '已关联 CAPEX', POSTED: '已入账' } as any)[value] || value;
const providerLabel = (value: string): string => ({ ALIPAY: '支付宝', WECHAT: '微信', CMB: '招商信用卡', CMB_SAVINGS: '招商储蓄卡' } as any)[value] || value || '旧版账单';
const channelLabel = (value: string): string => ({ ALIPAY: '支付宝', WECHAT: '微信', BANK_DIRECT: '银行直接消费', PLATFORM: '支付宝/微信（银行结算）' } as any)[value] || value || '未识别';
const fundingLabel = (value: string): string => ({ CREDIT_CARD: '信用卡', BANK_ACCOUNT: '银行账户', CASH: '现金' } as any)[value] || value || '未识别';
const dimensionLabel = (value: string): string => value === 'FUNDING_SOURCE' ? '资金来源' : value === 'MERCHANT_CHANNEL' ? '消费渠道' : '旧版账单';
const periodTypeLabel = (value: string): string => value === 'BILLING_CYCLE' ? '信用卡账单周期' : value === 'CALENDAR_MONTH' ? '自然月' : '旧版周期';
const coverageStatusLabel = (value: string): string => ({ PENDING: '待确认', VERIFIED: '已覆盖', CONFLICT: '有冲突' } as any)[value] || value || '待处理';
const statusColor = (value: string): string => ({ MATCHED: 'success', EVIDENCE: 'info', POSTED: 'success', CAPEX_LINKED: 'success', CLASSIFIED: 'primary', REVIEW: 'warning', UNMATCHED: 'warning' } as any)[value] || 'default';
function statementProvider(id: number): string { const item = dashboard.value.statements.find((v: any) => Number(v.Id || v.id) === Number(id)); return providerLabel(item?.Provider || item?.provider || ''); }
const manualCandidateItems = computed(() => manualCandidates.value.map((item: any) => ({ title: `${item.transaction_date || item.TransactionDate} · ${money(item.amount_minor ?? item.AmountMinor)} · ${item.comment || item.Comment || '无备注'}`, value: String(item.transaction_id || item.TransactionId) })));
const manualActionItems = [{ title: '匹配已有主账本交易', value: 'MATCH' }, { title: '确认不进入主账本', value: 'NO_LEDGER' }];
async function load(): Promise<void> { const [dash, accounts] = await Promise.all([axios.get(`v1/hengcai/reconciliation/dashboard.json?month=${month.value}`), axios.get('v1/accounts/list.json?visible_only=false')]); const result = dash.data.result || {}; dashboard.value = { ...result, statements: Array.isArray(result.statements) ? result.statements : [], lines: Array.isArray(result.lines) ? result.lines : [], manual_markers: Array.isArray(result.manual_markers) ? result.manual_markers : [], coverages: Array.isArray(result.coverages) ? result.coverages : [], categories: Array.isArray(result.categories) ? result.categories : [], capex_purchases: Array.isArray(result.capex_purchases) ? result.capex_purchases : [], capex_installments: Array.isArray(result.capex_installments) ? result.capex_installments : [], capex_settlements: Array.isArray(result.capex_settlements) ? result.capex_settlements : [], status_counts: result.status_counts || {} }; currentPage.value = Math.min(currentPage.value, pageCount.value); coreAccounts.value = flattenAccounts(accounts.data.result || []); }
async function changeMonth(): Promise<void> { currentPage.value = 1; await load(); }
async function upload(): Promise<void> { if (!selectedFile.value) return; uploading.value = true; message.value = ''; try { const form = new FormData(); form.append('file', selectedFile.value); form.append('provider', uploadForm.value.provider); form.append('account_id', uploadForm.value.account_id); await axios.post('v1/hengcai/reconciliation/upload.json', form); await load(); tab.value = 'workspace'; messageType.value = 'success'; message.value = '账单已解析，并完成跨来源去重与手工交易匹配'; files.value = null; } catch (ex: any) { messageType.value = 'error'; message.value = ex?.response?.data?.errorMessage || '账单上传失败'; } finally { uploading.value = false; } }
async function classifyAll(): Promise<void> { classifying.value = true; try { for (const item of dashboard.value.statements) { const form = new FormData(); form.append('statement_id', String(item.Id || item.id)); form.append('use_ai', 'true'); await axios.post('v1/hengcai/statements/classify.json', form, { timeout: DEFAULT_LLM_API_TIMEOUT }); } await load(); messageType.value = 'success'; message.value = '分类建议已生成，请确认后入账'; } catch (ex: any) { messageType.value = 'error'; message.value = ex?.code === 'ECONNABORTED' ? '分类请求超时，后端可能仍在处理，请稍后刷新查看' : ex?.response?.data?.errorMessage || '分类失败'; } finally { classifying.value = false; } }
async function saveCategory(line: any, value: any): Promise<void> { const id = String(value || ''); if (!id) return; await axios.put(`v1/hengcai/statements/lines/${line.Id || line.id}/classification.json`, { category_id: id, label: '人工确认', confidence_bps: 10000 }); await load(); }
function openRefundDialog(line: any): void { refundLine.value = line; refundCategoryId.value = null; refundDialog.value = true; }
async function confirmRefund(): Promise<void> { if (!refundLine.value || !refundCategoryId.value) return; refundBusy.value = true; try { await axios.put(`v1/hengcai/statements/lines/${lineId(refundLine.value)}/refund.json`, { category_id: String(refundCategoryId.value) }); refundDialog.value = false; await load(); messageType.value = 'success'; message.value = '已转为退款，将按负支出冲减，不计入收入'; } catch (ex: any) { messageType.value = 'error'; message.value = ex?.response?.data?.errorMessage || '退款处理失败'; } finally { refundBusy.value = false; } }
async function openManualDialog(line: any): Promise<void> { manualLine.value = line; manualCandidates.value = []; manualCandidateId.value = null; manualDialog.value = true; manualBusy.value = true; try { const response = await axios.get(`v1/hengcai/reconciliation/line-candidates.json?line_id=${lineId(line)}`); manualCandidates.value = response.data.result || []; } catch (ex: any) { messageType.value = 'error'; message.value = ex?.response?.data?.errorMessage || '候选交易加载失败'; manualDialog.value = false; } finally { manualBusy.value = false; } }
async function chooseManualAction(line: any, action: string): Promise<void> { if (!action) return; await openManualDialog(line); }
async function confirmManualMatch(): Promise<void> { if (!manualLine.value || !manualCandidateId.value) return; manualBusy.value = true; try { await axios.put(`v1/hengcai/statements/lines/${lineId(manualLine.value)}/manual-match.json`, { transaction_id: String(manualCandidateId.value) }); manualDialog.value = false; await load(); messageType.value = 'success'; message.value = '已人工匹配，并保留主账本原交易 ID'; } catch (ex: any) { messageType.value = 'error'; message.value = ex?.response?.data?.errorMessage || '人工匹配失败'; } finally { manualBusy.value = false; } }
async function confirmManualReview(): Promise<void> { if (!manualLine.value) return; manualBusy.value = true; try { await axios.put(`v1/hengcai/statements/lines/${lineId(manualLine.value)}/manual-review.json`, { action: 'CONFIRM_NO_LEDGER' }); manualDialog.value = false; await load(); messageType.value = 'success'; message.value = '已确认该转账/还款不进入主账本'; } catch (ex: any) { messageType.value = 'error'; message.value = ex?.response?.data?.errorMessage || '人工确认失败'; } finally { manualBusy.value = false; } }
async function resolvePlatformLine(line: any, action: 'CONFIRM_PLATFORM_LEDGER'|'CONFIRM_PLATFORM_EVIDENCE'): Promise<void> { try { await axios.put(`v1/hengcai/statements/lines/${lineId(line)}/manual-review.json`, { action }); await load(); messageType.value = 'success'; message.value = action === 'CONFIRM_PLATFORM_LEDGER' ? '已确认这笔银行流水独立入账' : '已确认由支付宝/微信明细覆盖，银行流水仅保留为证据'; } catch (ex: any) { messageType.value = 'error'; message.value = ex?.response?.data?.errorMessage || '平台重复风险确认失败'; } }
async function linkCapex(line: any, installmentId: any): Promise<void> { if (!installmentId) return; try { await axios.put(`v1/hengcai/statements/lines/${lineId(line)}/capex.json`, { installment_id: String(installmentId) }); await load(); messageType.value = 'success'; message.value = '分期本金已关联 CAPEX 期次'; } catch (ex: any) { messageType.value = 'error'; message.value = ex?.response?.data?.errorMessage || 'CAPEX 关联失败'; } }
function shiftedMonth(dateText: string, offset: number): string { const source = new Date(`${dateText}T00:00:00Z`); const day = source.getUTCDate(); const target = new Date(Date.UTC(source.getUTCFullYear(), source.getUTCMonth() + offset, 1)); const last = new Date(Date.UTC(target.getUTCFullYear(), target.getUTCMonth() + 1, 0)).getUTCDate(); target.setUTCDate(Math.min(day, last)); return target.toISOString().slice(0, 10); }
function openCapexDialog(line: any): void { const description = String(line.Description || line.description || '信用卡分期'); const matched = description.match(/第(\d+)\/(\d+)期/); const currentNo = Number(matched?.[1] || 1); const count = Number(matched?.[2] || 1); const principalMinor = Math.abs(Number(line.AmountMinor ?? line.amount_minor ?? 0)); const postedDate = String(line.PostedDate || line.posted_date || new Date().toISOString().slice(0, 10)); const purchaseDate = String(line.TransactionDate || line.transaction_date || postedDate); const itemName = description.replace(/\s*本金\s*第\d+\/\d+期.*/, '').replace(/^消费分期[-－]?/, '') || '信用卡分期'; capexTargetLine.value = line; capexTargetNo.value = currentNo; capexDialogMessage.value = ''; capexForm.value = { item_name: itemName, purchase_date: purchaseDate, total_amount_yuan: principalMinor * count / 100, down_payment_yuan: 0, installment_count: count, first_due_date: shiftedMonth(postedDate, -(currentNo - 1)), financing_type: 'INSTALLMENT', currency: 'CNY', note: `由账单流水 #${line.LineNumber || line.line_number} 创建，金额为账单估算值` }; capexDialog.value = true; }
async function createAndLinkCapex(): Promise<void> { if (!capexTargetLine.value || !capexFormValid.value) { capexDialogMessage.value = '请检查金额、日期和期次；金额按元填写，最多两位小数'; return; } savingCapex.value = true; capexDialogMessage.value = ''; try { const payload = { item_name: capexForm.value.item_name.trim(), purchase_date: capexForm.value.purchase_date, total_amount_minor: Math.round(capexTotalAmountYuan.value * 100), down_payment_minor: Math.round(Number(capexForm.value.down_payment_yuan) * 100), installment_count: Number(capexForm.value.installment_count), first_due_date: capexForm.value.first_due_date, financing_type: capexForm.value.financing_type, interest_fee_total_minor: 0, currency: capexForm.value.currency, note: capexForm.value.note }; const response = await axios.post('v1/hengcai/capex/add.json', payload); const installments = response.data.result?.installments || []; const target = installments.find((item: any) => Number(item.installment_no || item.InstallmentNo) === Number(capexTargetNo.value)); if (!target) throw new Error('新建项目未生成对应期次'); await axios.put(`v1/hengcai/statements/lines/${lineId(capexTargetLine.value)}/capex.json`, { installment_id: String(target.id || target.Id) }); capexDialog.value = false; await load(); messageType.value = 'success'; message.value = 'CAPEX 项目已创建，本期本金已完成关联'; } catch (ex: any) { capexDialogMessage.value = ex?.response?.data?.errorMessage || ex?.message || 'CAPEX 创建失败'; } finally { savingCapex.value = false; } }
function openUnpostDialog(statement: any): void { unpostStatement.value = statement; unpostReason.value = '修改账单凭据'; unpostDialog.value = true; }
async function confirmUnpost(): Promise<void> { if (!unpostStatement.value || !unpostReason.value.trim()) return; unposting.value = true; message.value = ''; try { const response = await axios.post('v1/hengcai/statements/unpost.json', { statement_id: String(statementId(unpostStatement.value)), month: month.value, reason: unpostReason.value.trim() }); const result = response.data.result || {}; unpostDialog.value = false; await load(); tab.value = 'workspace'; messageType.value = 'success'; message.value = `反入账完成：撤销 ${result.deleted_transactions || 0} 笔账单创建交易，解除 ${result.restored_evidence || 0} 条凭据关联，回退 ${result.reverted_capex || 0} 条 CAPEX 核销`; } catch (ex: any) { messageType.value = 'error'; message.value = ex?.response?.data?.errorMessage || '账单反入账失败'; } finally { unposting.value = false; } }
async function postAll(): Promise<void> { posting.value = true; message.value = ''; try { for (const item of dashboard.value.statements) { await axios.post('v1/hengcai/statements/post.json', { statement_id: String(item.Id || item.id), month: month.value }); } await load(); messageType.value = 'success'; message.value = '已完成确认入账；匹配到的手工交易保留原 ID，平台结算流水没有重复入账'; } catch (ex: any) { messageType.value = 'error'; message.value = ex?.response?.data?.errorMessage || '确认入账失败'; } finally { posting.value = false; } }
async function closeMonth(): Promise<void> { try { await axios.post('v1/hengcai/months/close.json', { month: month.value, note: '对账工作台月结' }); await load(); messageType.value = 'success'; message.value = `${month.value} 已完成月结`; } catch (ex: any) { messageType.value = 'error'; message.value = ex?.response?.data?.errorMessage || '月结失败'; } }
async function reopen(): Promise<void> { await axios.post('v1/hengcai/months/reopen.json', { month: month.value }); await load(); messageType.value = 'info'; message.value = `${month.value} 已重新打开`; }
onMounted(() => load().catch(() => { messageType.value = 'error'; message.value = '对账数据加载失败'; }));
</script>

<style scoped>
.month-selector { flex: 0 1 190px; min-width: 170px; }.table-wrap { overflow-x: auto; }.description-cell { min-width: 220px; max-width: 420px; white-space: normal; }.sortable-header { display: inline-flex; align-items: center; gap: 2px; width: 100%; white-space: nowrap; }.sort-arrow { font-size: 14px; line-height: 1; }
</style>
