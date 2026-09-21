const { createApp } = Vue;

async function api(path, opts = {}) {
  const r = await fetch('/api' + path, {
    method: opts.method || 'GET',
    headers: { 'Content-Type': 'application/json' },
    body: opts.body !== undefined ? JSON.stringify(opts.body) : undefined,
  });
  let data = {};
  try { data = await r.json(); } catch (e) { /* 空响应 */ }
  if (!r.ok) {
    const err = new Error(data.error || ('HTTP ' + r.status));
    err.status = r.status;
    throw err;
  }
  return data;
}

createApp({
  data() {
    return {
      authed: false,
      password: '',
      loginErr: '',
      err: '',
      tab: 'channels',
      tabs: [
        { key: 'channels', label: '渠道' },
        { key: 'models', label: '模型与别名' },
        { key: 'tokens', label: 'Token' },
        { key: 'logs', label: '日志' },
      ],
      channels: [],
      models: [],
      tokens: [],
      logs: { data: [], total: 0, page: 1, size: 20 },
      logFilter: { model: '', status: '', token_id: '', channel_id: '' },
      chForm: null,
      fetchDlg: null,
      testResult: null,
      modelForm: null,
      newTokenName: '',
      createdToken: null,
      logDetail: null,
    };
  },
  computed: {
    testModelOptions() {
      if (!this.chForm) return [];
      return this.chForm.models
        .map(b => {
          const m = this.models.find(x => x.id === b.model_id);
          return b.upstream_model || (m ? m.name : '');
        })
        .filter(Boolean);
    },
    logPages() {
      return Math.max(1, Math.ceil(this.logs.total / this.logs.size));
    },
    selAttempt() {
      if (!this.logDetail) return null;
      return this.logDetail.attempts.find(a => a.id === this.logDetail.sel) || this.logDetail.data;
    },
  },
  methods: {
    async guard(fn) {
      try {
        await fn();
      } catch (e) {
        if (e.status === 401) { this.authed = false; return; }
        this.err = e.message;
        setTimeout(() => { this.err = ''; }, 5000);
      }
    },
    async login() {
      this.loginErr = '';
      try {
        await api('/login', { method: 'POST', body: { password: this.password } });
        this.authed = true;
        this.password = '';
        await this.loadAll();
      } catch (e) {
        this.loginErr = '登录失败：' + e.message;
      }
    },
    async logout() {
      await this.guard(async () => { await api('/logout', { method: 'POST' }); });
      this.authed = false;
    },
    switchTab(k) {
      this.tab = k;
      if (k === 'logs') this.loadLogs();
    },
    async loadAll() {
      await this.guard(async () => {
        const [ch, m, tk] = await Promise.all([
          api('/channels'), api('/models'), api('/tokens'),
        ]);
        this.channels = ch.data || [];
        this.models = m.data || [];
        this.tokens = tk.data || [];
        await this.loadLogs();
      });
    },
    async loadChannels() { await this.guard(async () => { this.channels = (await api('/channels')).data || []; }); },
    async loadModels() { await this.guard(async () => { this.models = (await api('/models')).data || []; }); },
    async loadTokens() { await this.guard(async () => { this.tokens = (await api('/tokens')).data || []; }); },
    async loadLogs() {
      await this.guard(async () => {
        const q = new URLSearchParams({ page: this.logs.page, size: this.logs.size });
        if (this.logFilter.model) q.set('model', this.logFilter.model);
        if (this.logFilter.status) q.set('status', this.logFilter.status);
        if (this.logFilter.token_id) q.set('token_id', this.logFilter.token_id);
        if (this.logFilter.channel_id) q.set('channel_id', this.logFilter.channel_id);
        const v = await api('/logs?' + q);
        this.logs.data = v.data || [];
        this.logs.total = v.total;
      });
    },
    searchLogs() { this.logs.page = 1; this.loadLogs(); },
    pageLogs(d) { this.logs.page += d; this.loadLogs(); },

    // ---- 渠道 ----
    openChannelForm(c) {
      if (c) {
        this.chForm = {
          id: c.id, name: c.name, base_url: c.base_url, api_key: c.api_key,
          priority: c.priority, enabled: c.enabled, test_model: c.test_model,
          models: (c.models || []).map(b => ({ model_id: b.model_id, upstream_model: b.upstream_model })),
        };
      } else {
        this.chForm = { name: '', base_url: '', api_key: '', priority: 0, enabled: true, test_model: '', models: [] };
      }
    },
    addBinding() {
      this.chForm.models.push({ model_id: this.models.length ? this.models[0].id : 0, upstream_model: '' });
    },
    async saveChannel() {
      await this.guard(async () => {
        const f = this.chForm;
        const body = {
          name: f.name, base_url: f.base_url, api_key: f.api_key,
          priority: f.priority, enabled: f.enabled, test_model: f.test_model,
          models: f.models.filter(b => b.model_id),
        };
        if (f.id) await api('/channels/' + f.id, { method: 'PUT', body });
        else await api('/channels', { method: 'POST', body });
        this.chForm = null;
        await this.loadChannels();
      });
    },
    async delChannel(c) {
      if (!confirm('删除渠道「' + c.name + '」？')) return;
      await this.guard(async () => {
        await api('/channels/' + c.id, { method: 'DELETE' });
        await this.loadChannels();
      });
    },
    async toggleChannel(c) {
      await this.guard(async () => {
        await api('/channels/' + c.id + '/toggle', { method: 'POST', body: { enabled: !c.enabled } });
        await this.loadChannels();
      });
    },
    async resetChannel(c) {
      await this.guard(async () => {
        await api('/channels/' + c.id + '/reset', { method: 'POST' });
        await this.loadChannels();
      });
    },
    async fetchModels(c) {
      await this.guard(async () => {
        const v = await api('/channels/' + c.id + '/fetch-models', { method: 'POST' });
        this.fetchDlg = {
          channelID: c.id,
          rows: (v.data || []).map(r => ({
            checked: true,
            upstream_id: r.upstream_id,
            context_length: r.context_length,
            max_output_tokens: r.max_output_tokens,
            suggested_name: r.suggested_name,
            target: r.suggested_model_id || '__new__',
          })),
        };
      });
    },
    async saveFetch() {
      await this.guard(async () => {
        const bindings = this.fetchDlg.rows.filter(r => r.checked).map(r => {
          const b = { upstream_model: r.upstream_id, context_length: r.context_length, max_output_tokens: r.max_output_tokens };
          if (r.target === '__new__') b.new_model_name = r.suggested_name;
          else b.model_id = r.target;
          return b;
        });
        if (!bindings.length) { this.fetchDlg = null; return; }
        await api('/channels/' + this.fetchDlg.channelID + '/models', { method: 'POST', body: { bindings } });
        this.fetchDlg = null;
        await Promise.all([this.loadChannels(), this.loadModels()]);
      });
    },
    async testChannel(c) {
      this.testResult = { loading: true, channel: c.name, result: null };
      await this.guard(async () => {
        const v = await api('/channels/' + c.id + '/test', { method: 'POST' });
        this.testResult = { loading: false, channel: c.name, result: v };
      });
      if (this.testResult && this.testResult.loading) this.testResult = null;
    },

    // ---- 模型 ----
    openModelForm(m) {
      if (m) {
        this.modelForm = {
          id: m.id, name: m.name, aliasesText: (m.aliases || []).join('\n'),
          context_length: m.context_length, max_output_tokens: m.max_output_tokens,
        };
      } else {
        this.modelForm = { name: '', aliasesText: '', context_length: null, max_output_tokens: null };
      }
    },
    async saveModel() {
      await this.guard(async () => {
        const f = this.modelForm;
        const body = {
          name: f.name,
          aliases: f.aliasesText.split('\n').map(s => s.trim()).filter(Boolean),
          context_length: f.context_length || null,
          max_output_tokens: f.max_output_tokens || null,
        };
        if (f.id) await api('/models/' + f.id, { method: 'PUT', body });
        else await api('/models', { method: 'POST', body });
        this.modelForm = null;
        await this.loadModels();
      });
    },
    async delModel(m) {
      if (!confirm('删除模型「' + m.name + '」？相关渠道绑定会一并移除。')) return;
      await this.guard(async () => {
        await api('/models/' + m.id, { method: 'DELETE' });
        await Promise.all([this.loadModels(), this.loadChannels()]);
      });
    },

    // ---- Token ----
    async createToken() {
      if (!this.newTokenName.trim()) return;
      await this.guard(async () => {
        const v = await api('/tokens', { method: 'POST', body: { name: this.newTokenName.trim() } });
        this.createdToken = v;
        this.newTokenName = '';
        await this.loadTokens();
      });
    },
    async toggleToken(t) {
      await this.guard(async () => {
        await api('/tokens/' + t.id, { method: 'PUT', body: { name: t.name, enabled: !t.enabled } });
        await this.loadTokens();
      });
    },
    async delToken(t) {
      if (!confirm('删除 token「' + t.name + '」？使用它的客户端会立即失效。')) return;
      await this.guard(async () => {
        await api('/tokens/' + t.id, { method: 'DELETE' });
        await this.loadTokens();
      });
    },
    copyToken() {
      navigator.clipboard && navigator.clipboard.writeText(this.createdToken.token);
    },

    // ---- 日志 ----
    async openLogDetail(l) {
      await this.guard(async () => {
        const v = await api('/logs/' + l.id);
        this.logDetail = { data: v.data, attempts: v.attempts || [v.data], sel: v.data.id };
      });
    },

    // ---- 工具 ----
    fmtTime(s) {
      if (!s) return '-';
      const d = new Date(s);
      return isNaN(d) ? s : d.toLocaleString();
    },
    pretty(s) {
      if (!s) return '(空)';
      try { return JSON.stringify(JSON.parse(s), null, 2); } catch (e) { return s; }
    },
  },
  async mounted() {
    try {
      await api('/me');
      this.authed = true;
      await this.loadAll();
    } catch (e) { /* 未登录，停在登录页 */ }
  },
}).mount('#app');
