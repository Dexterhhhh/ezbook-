<template>
    <div>
        <v-card class="mb-4">
            <v-card-title class="d-flex flex-wrap align-center ga-3"><span>现金流预测</span><v-spacer/><v-btn variant="tonal" href="#/hengcai/reconciliation">前往账单对账</v-btn></v-card-title>
            <v-card-subtitle>预测收入使用工资与绩效；实际收入由已入账的账单对账流水校准</v-card-subtitle>
            <v-card-text>
                <v-alert v-if="message" :type="messageType" variant="tonal" closable class="mb-4" @click:close="message=''">{{ message }}</v-alert>
                <v-alert type="info" variant="tonal" class="mb-4">未来月份不再使用历史收入平均值。月度绩效每月计入，季度绩效在 3、6、9、12 月计入，年度绩效在指定月份计入；当前及历史月份只采用已完成入账的对账收入。</v-alert>
                <v-row class="mb-1">
                    <v-col cols="12" sm="6" lg="4"><v-card variant="tonal"><v-card-text><div class="text-medium-emphasis">预估月薪</div><div class="text-h5">{{ money(setting.monthly_salary_minor) }}</div><div class="text-caption">未来收入基线</div></v-card-text></v-card></v-col>
                    <v-col cols="12" sm="6" lg="4"><v-card variant="tonal" color="primary"><v-card-text><div class="text-medium-emphasis">月度 / 季度绩效</div><div class="text-h5">{{ money(setting.monthly_performance_minor) }} / {{ money(setting.quarterly_performance_minor) }}</div><div class="text-caption">每月 / 每季度末发放</div></v-card-text></v-card></v-col>
                    <v-col cols="12" sm="6" lg="4"><v-card variant="tonal" color="primary"><v-card-text><div class="text-medium-emphasis">年度绩效</div><div class="text-h5">{{ money(setting.annual_performance_minor) }}</div><div class="text-caption">预计每年 {{ setting.performance_month || '—' }} 月到账</div></v-card-text></v-card></v-col>
                    <v-col cols="12" sm="6" lg="4"><v-card variant="tonal" color="success"><v-card-text><div class="text-medium-emphasis">年度预测收入</div><div class="text-h5">{{ money(annualForecastMinor) }}</div><div class="text-caption">工资 + 月度、季度、年度绩效</div></v-card-text></v-card></v-col>
                    <v-col cols="12" sm="6" lg="4"><v-card variant="tonal" color="info"><v-card-text><div class="text-medium-emphasis">本月实际收入</div><div class="text-h5">{{ money(currentActual?.income_minor || 0) }}</div><div class="text-caption">{{ currentActual?.line_count || 0 }} 条已入账对账流水</div></v-card-text></v-card></v-col>
                </v-row>
                <v-form @submit.prevent="saveBasis"><v-row>
                    <v-col cols="12" sm="6" lg="3"><v-text-field v-model.number="basisForm.monthly_salary_yuan" type="number" min="0" step="0.01" label="预估税后月薪（元）" hint="每月计入" persistent-hint/></v-col>
                    <v-col cols="12" sm="6" lg="3"><v-text-field v-model.number="basisForm.monthly_performance_yuan" type="number" min="0" step="0.01" label="月度绩效（元/月）" hint="每月计入一次" persistent-hint/></v-col>
                    <v-col cols="12" sm="6" lg="3"><v-text-field v-model.number="basisForm.quarterly_performance_yuan" type="number" min="0" step="0.01" label="季度绩效（元/季度）" hint="3、6、9、12 月计入" persistent-hint/></v-col>
                    <v-col cols="12" sm="6" lg="3"><v-text-field v-model.number="basisForm.annual_performance_yuan" type="number" min="0" step="0.01" label="年度绩效（元/年）" hint="指定月份计入一次" persistent-hint/></v-col>
                    <v-col cols="12" sm="6" lg="3"><v-select v-model.number="basisForm.performance_month" :items="monthOptions" item-title="title" item-value="value" label="年度绩效发放月份"/></v-col>
                    <v-col cols="12" sm="6" lg="3" class="d-flex align-center"><v-btn type="submit" color="primary" block :loading="savingBasis" :disabled="!basisValid">保存预测口径</v-btn></v-col>
                </v-row></v-form>
            </v-card-text>
        </v-card>

        <v-card class="mb-4">
            <v-card-title class="d-flex flex-wrap align-center ga-3"><span>实际收入校准</span><v-spacer/><v-btn variant="tonal" :loading="loading" @click="refresh">按最新对账刷新</v-btn></v-card-title>
            <v-card-subtitle>只统计状态为“已入账”的收入流水；需要修改实际收入时，请在对账模块修正或重新入账</v-card-subtitle>
            <v-table density="comfortable"><thead><tr><th>月份</th><th class="text-end">实际收入</th><th class="text-end">收入流水</th><th>校准状态</th><th>来源</th></tr></thead><tbody>
                <tr v-for="item in [...actuals].reverse()" :key="item.month"><td>{{ item.month }}</td><td class="text-end">{{ money(item.income_minor) }}</td><td class="text-end">{{ item.line_count }} 条</td><td><v-chip size="small" :color="item.closed ? 'success' : item.line_count ? 'info' : 'warning'">{{ item.closed ? '已关账' : item.line_count ? '已按对账校准' : '待收入对账' }}</v-chip></td><td>账单对账 · 已入账收入</td></tr>
            </tbody></v-table>
        </v-card>

        <v-card class="mb-4">
            <v-card-title>当前及未来 12 个月</v-card-title>
            <v-card-subtitle>收入采用工资绩效/对账双口径，支出采用主账本实际值或近三个月均值，CAPEX 自动联动</v-card-subtitle>
            <v-table density="comfortable"><thead><tr><th>月份</th><th>口径</th><th class="text-end">收入</th><th class="text-end">运营支出</th><th class="text-end">CAPEX</th><th class="text-end">投资收益</th><th class="text-end">自由现金流</th><th>数据来源</th></tr></thead><tbody><tr v-if="!rows.length"><td colspan="8" class="text-center py-6">暂无预测记录</td></tr><tr v-for="item in rows" :key="item.month"><td>{{ item.month }}</td><td><v-chip size="small" :color="item.data_type === 'FORECAST' ? 'primary' : 'success'">{{ dataTypeLabel(item.data_type) }}</v-chip></td><td class="text-end">{{ money(item.income_minor) }}</td><td class="text-end">{{ money(item.opex_minor) }}</td><td class="text-end">{{ money(item.capex_minor) }}</td><td class="text-end">{{ money(item.investment_return_minor) }}</td><td class="text-end" :class="item.free_cashflow_minor >= 0 ? 'text-success' : 'text-warning'">{{ money(item.free_cashflow_minor) }}</td><td class="explanation-cell">{{ item.explanation }}</td></tr></tbody></v-table>
        </v-card>

        <v-card>
            <v-card-title>单月补充调整</v-card-title>
            <v-card-subtitle>投资收益与期末资产可单独保存；收入不在这里手工覆盖</v-card-subtitle>
            <v-card-text><v-form @submit.prevent="calculate"><v-row>
                <v-col cols="12" sm="4"><v-text-field v-model="adjustmentForm.month" label="月份" placeholder="2026-08" required/></v-col>
                <v-col cols="12" sm="3"><v-text-field v-model.number="adjustmentForm.investment_return_yuan" label="投资收益调整（元）" type="number" step="0.01"/></v-col>
                <v-col cols="12" sm="3"><v-text-field v-model.number="adjustmentForm.ending_assets_yuan" label="期末资产（元）" type="number" step="0.01"/></v-col>
                <v-col cols="12" sm="2" class="d-flex align-center"><v-btn type="submit" color="primary" block :loading="savingAdjustment">保存调整</v-btn></v-col>
            </v-row></v-form></v-card-text>
        </v-card>
    </div>
</template>

<script setup lang="ts">
import axios from 'axios';
import { computed, onMounted, ref } from 'vue';

const currentMonth = new Date().toISOString().slice(0, 7);
const rows = ref<any[]>([]), actuals = ref<any[]>([]); const loading = ref(false), savingBasis = ref(false), savingAdjustment = ref(false); const message = ref(''); const messageType = ref<'success'|'error'>('success');
const setting = ref({ monthly_salary_minor: 0, monthly_performance_minor: 0, quarterly_performance_minor: 0, annual_performance_minor: 0, performance_month: 1 });
const basisForm = ref({ monthly_salary_yuan: 0, monthly_performance_yuan: 0, quarterly_performance_yuan: 0, annual_performance_yuan: 0, performance_month: 1 });
const adjustmentForm = ref({ month: currentMonth, investment_return_yuan: 0, ending_assets_yuan: 0 });
const monthOptions = Array.from({length:12},(_,index)=>({title:`${index+1} 月`,value:index+1}));
const money = (minor:number)=>new Intl.NumberFormat('zh-CN',{style:'currency',currency:'CNY'}).format(Number(minor||0)/100);
const basisValid = computed(()=>Number(basisForm.value.monthly_salary_yuan)>=0&&Number(basisForm.value.monthly_performance_yuan)>=0&&Number(basisForm.value.quarterly_performance_yuan)>=0&&Number(basisForm.value.annual_performance_yuan)>=0&&Number.isInteger(Number(basisForm.value.performance_month))&&Number(basisForm.value.performance_month)>=1&&Number(basisForm.value.performance_month)<=12);
const annualForecastMinor = computed(()=>Number(setting.value.monthly_salary_minor||0)*12+Number(setting.value.monthly_performance_minor||0)*12+Number(setting.value.quarterly_performance_minor||0)*4+Number(setting.value.annual_performance_minor||0));
const currentActual = computed(()=>actuals.value.find(item=>item.month===currentMonth));
const dataTypeLabel=(type:string)=>type==='FORECAST'?'工资绩效预测':type==='MTD_ACTUAL'?'本月至今实际':'对账实际';
function syncBasisForm(){basisForm.value={monthly_salary_yuan:Number(setting.value.monthly_salary_minor||0)/100,monthly_performance_yuan:Number(setting.value.monthly_performance_minor||0)/100,quarterly_performance_yuan:Number(setting.value.quarterly_performance_minor||0)/100,annual_performance_yuan:Number(setting.value.annual_performance_minor||0)/100,performance_month:Number(setting.value.performance_month||1)}}
async function load(){const[projectionResponse,basisResponse]=await Promise.all([axios.get('v1/hengcai/projections/list.json'),axios.get('v1/hengcai/projections/income-basis.json')]);rows.value=projectionResponse.data.result||[];const result=basisResponse.data.result||{};setting.value=result.setting||setting.value;actuals.value=result.actuals||[];syncBasisForm()}
async function refresh(){loading.value=true;message.value='';try{await load();messageType.value='success';message.value='已按最新入账对账流水刷新实际收入'}catch(ex:any){messageType.value='error';message.value=ex?.response?.data?.errorMessage||'收入数据刷新失败'}finally{loading.value=false}}
async function saveBasis(){if(!basisValid.value)return;savingBasis.value=true;message.value='';try{await axios.put('v1/hengcai/projections/income-basis.json',{monthly_salary_minor:Math.round(Number(basisForm.value.monthly_salary_yuan)*100),monthly_performance_minor:Math.round(Number(basisForm.value.monthly_performance_yuan)*100),quarterly_performance_minor:Math.round(Number(basisForm.value.quarterly_performance_yuan)*100),annual_performance_minor:Math.round(Number(basisForm.value.annual_performance_yuan)*100),performance_month:Number(basisForm.value.performance_month)});await load();messageType.value='success';message.value='工资与绩效预测口径已保存'}catch(ex:any){messageType.value='error';message.value=ex?.response?.data?.errorMessage||'预测口径保存失败'}finally{savingBasis.value=false}}
async function calculate(){savingAdjustment.value=true;message.value='';try{await axios.post('v1/hengcai/projections/calculate.json',{month:adjustmentForm.value.month,investment_return_minor:Math.round(Number(adjustmentForm.value.investment_return_yuan||0)*100),ending_assets_minor:Math.round(Number(adjustmentForm.value.ending_assets_yuan||0)*100)});await load();messageType.value='success';message.value='单月补充调整已保存'}catch(ex:any){messageType.value='error';message.value=ex?.response?.data?.errorMessage||'调整保存失败'}finally{savingAdjustment.value=false}}
onMounted(()=>load().catch(()=>{messageType.value='error';message.value='现金流预测数据加载失败'}));
</script>

<style scoped>
.explanation-cell{min-width:280px;max-width:420px;white-space:normal}
</style>
