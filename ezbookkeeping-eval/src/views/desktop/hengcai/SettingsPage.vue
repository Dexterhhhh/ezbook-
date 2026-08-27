<template>
    <div>
        <v-card class="mb-4"><v-card-title>衡财基础设置</v-card-title><v-card-subtitle>只读拉取 Alpaca 行情，不执行交易</v-card-subtitle><v-card-text>
            <v-alert type="info" variant="tonal" class="mb-4">本页只使用 Alpaca Market Data API：仅向 https://data.alpaca.markets 发送 GET 请求，不访问账户、订单或下单接口。系统每天最多同步一次，使用最近完整交易日的收盘价。密钥在服务端加密保存。</v-alert>
            <v-alert v-if="message" :type="messageType" variant="tonal" class="mb-4">{{ message }}</v-alert>
            <v-form @submit.prevent="save($event)"><v-row><v-col cols="12" md="3"><v-select v-model="form.environment" name="environment" label="密钥类型（仅标记）" :items="['PAPER', 'LIVE']" hint="不会调用任何交易接口" persistent-hint /></v-col><v-col cols="12" md="4"><v-text-field v-model="form.api_key_id" name="api_key_id" label="Alpaca API Key ID" autocomplete="username" /></v-col><v-col cols="12" md="5"><v-text-field v-model="form.secret_key" name="secret_key" label="Alpaca Secret Key" type="password" autocomplete="current-password" :hint="saved ? '留空表示保留已加密保存的 Secret Key' : '首次保存时必填'" persistent-hint /></v-col><v-col cols="12"><v-text-field v-model="form.data_url" name="data_url" label="Market Data API 地址（只读）" hint="官方地址：https://data.alpaca.markets" persistent-hint /></v-col><v-col cols="12" class="d-flex ga-3"><v-btn type="submit" color="primary" :loading="saving">保存设置</v-btn><v-btn variant="tonal" :loading="testing" :disabled="!saved" @click="test">测试只读行情</v-btn></v-col></v-row></v-form>
        </v-card-text></v-card>
        <v-card><v-card-title>AI 分类 API</v-card-title><v-card-subtitle>仅用于对账工作台的交易分类建议，不自动入账</v-card-subtitle><v-card-text>
            <v-alert type="info" variant="tonal" class="mb-4">API Key 在服务端加密并按当前用户隔离保存。支持 OpenAI 兼容 API；只有点击“生成分类建议”时才发送商户摘要和候选分类。</v-alert>
            <v-alert v-if="aiMessage" :type="aiMessageType" variant="tonal" class="mb-4">{{ aiMessage }}</v-alert>
            <v-form @submit.prevent="saveAI"><v-row>
                <v-col cols="12" md="2"><v-switch v-model="aiForm.enabled" label="启用 AI 分类" color="primary"/></v-col>
                <v-col cols="12" md="3"><v-select v-model="aiForm.provider" label="提供商" :items="[{ title: 'OpenAI 兼容 API', value: 'openai_compatible' }]"/></v-col>
                <v-col cols="12" md="7"><v-text-field v-model="aiForm.base_url" label="API 基础地址" hint="例如 https://api.openai.com/v1" persistent-hint/></v-col>
                <v-col cols="12" md="5"><v-text-field v-model="aiForm.model" label="模型" hint="例如 gpt-4.1-mini" persistent-hint/></v-col>
                <v-col cols="12" md="7"><v-text-field v-model="aiForm.api_key" label="AI API Key" type="password" :hint="aiConfigured ? '留空表示保留已加密的 Key' : '首次启用时必填'" persistent-hint autocomplete="new-password"/></v-col>
                <v-col cols="12" class="d-flex ga-3"><v-btn type="submit" color="primary" :loading="aiSaving">保存 AI 设置</v-btn><v-btn variant="tonal" :loading="aiTesting" :disabled="!aiConfigured" @click="testAI">测试 AI 连接</v-btn></v-col>
            </v-row></v-form>
        </v-card-text></v-card>
    </div>
</template>

<script setup lang="ts">
import axios from 'axios';
import { onMounted, ref } from 'vue';
const form = ref({ environment: 'PAPER', api_key_id: '', secret_key: '', trading_url: 'https://paper-api.alpaca.markets', data_url: 'https://data.alpaca.markets' });
const saving = ref(false); const testing = ref(false); const saved = ref(false); const message = ref(''); const messageType = ref<'success' | 'error' | 'info'>('info');
const aiForm = ref({ enabled: false, provider: 'openai_compatible', base_url: 'https://api.openai.com/v1', model: 'gpt-4.1-mini', api_key: '' });
const aiSaving = ref(false); const aiTesting = ref(false); const aiConfigured = ref(false); const aiMessage = ref(''); const aiMessageType = ref<'success'|'error'|'info'>('info');
async function load(): Promise<void> { const response = await axios.get('v1/hengcai/settings/alpaca.json'); form.value = { ...form.value, ...(response.data.result || {}) }; saved.value = !!response.data.result?.configured; }
async function save(event?: Event): Promise<void> {
    if (event?.currentTarget instanceof HTMLFormElement) {
        const nativeForm = new FormData(event.currentTarget);
        form.value.api_key_id = String(nativeForm.get('api_key_id') || '').trim();
        form.value.secret_key = String(nativeForm.get('secret_key') || '');
        form.value.trading_url = String(nativeForm.get('trading_url') || '').trim();
        form.value.data_url = String(nativeForm.get('data_url') || '').trim();
    }
    saving.value = true;
    message.value = '';
    try {
        const response = await axios.put('v1/hengcai/settings/alpaca.json', form.value);
        saved.value = response.data.result?.configured === true;
        messageType.value = 'success';
        message.value = 'Alpaca 设置已安全保存，现在可以测试只读行情';
        form.value.secret_key = '';
    } catch (ex: any) {
        saved.value = false;
        messageType.value = 'error';
        message.value = ex?.response?.data?.errorMessage || '设置保存失败';
    } finally {
        saving.value = false;
    }
}
async function test(): Promise<void> { testing.value = true; message.value = ''; try { const response = await axios.post('v1/hengcai/settings/alpaca/test.json'); messageType.value = 'success'; message.value = response.data.result?.message || 'Alpaca 只读行情连接成功'; } catch (ex: any) { messageType.value = 'error'; const detail = ex?.response?.data?.errorMessage; message.value = !detail || detail === 'incomplete or incorrect submission' ? '只读行情连接失败，请检查 API Key、Secret Key 和行情 API 地址（应为 https://data.alpaca.markets）' : detail; } finally { testing.value = false; } }
async function loadAI(): Promise<void> { const response = await axios.get('v1/hengcai/settings/ai.json'); aiForm.value = { ...aiForm.value, ...(response.data.result || {}), api_key: '' }; aiConfigured.value = response.data.result?.configured === true; }
async function saveAI(): Promise<void> { aiSaving.value = true; aiMessage.value = ''; try { const response = await axios.put('v1/hengcai/settings/ai.json', aiForm.value); aiConfigured.value = response.data.result?.configured === true; aiForm.value.api_key = ''; aiMessageType.value = 'success'; aiMessage.value = 'AI 设置已加密保存'; } catch (ex: any) { aiMessageType.value = 'error'; aiMessage.value = ex?.response?.data?.errorMessage || 'AI 设置保存失败'; } finally { aiSaving.value = false; } }
async function testAI(): Promise<void> { aiTesting.value = true; aiMessage.value = ''; try { const response = await axios.post('v1/hengcai/settings/ai/test.json'); aiMessageType.value = 'success'; aiMessage.value = response.data.result?.message || 'AI API 连接成功'; } catch (ex: any) { aiMessageType.value = 'error'; aiMessage.value = ex?.response?.data?.errorMessage || 'AI API 连接失败'; } finally { aiTesting.value = false; } }
onMounted(() => { load().catch(() => { messageType.value = 'error'; message.value = 'Alpaca 设置加载失败'; }); loadAI().catch(() => { aiMessageType.value = 'error'; aiMessage.value = 'AI 设置加载失败'; }); });
</script>
