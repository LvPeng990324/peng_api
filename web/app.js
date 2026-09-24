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
      notice: '',
      toasts: [],
      tab: 'channels',
      sidebarOpen: false,
      tabs: [
        { key: 'channels', label: '渠道' },
        { key: 'models', label: '模型' },
        { key: 'mappings', label: '模型映射' },
        { key: 'tokens', label: 'Token' },
        { key: 'logs', label: '日志' },
      ],
      channels: [],
      models: [],
      mappings: [],
      tokens: [],
      logs: { data: [], total: 0, page: 1, size: 20 },
      logFilter: { model: '', status: '', token_id: '', channel_id: '', start_time: '', end_time: '' },
      chForm: null,
      chModelsDlg: null,
      mappingModelsDlg: null,
      fetchDlg: null,
      testResult: null,
      modelForm: null,
      mappingForm: null,
      newTokenName: '',
      createdToken: null,
      logDetail: null,
    };
  },
  computed: {
    tabLabel() {
      const t = this.tabs.find(x => x.key === this.tab);
      return t ? t.label : '';
    },
    testModelOptions() {
      if (!this.chForm || !this.chForm.id) return [];
      return this.models.filter(m => m.channel_id === this.chForm.id).map(m => m.name);
    },
    logPages() {
      return Math.max(1, Math.ceil(this.logs.total / this.logs.size));
    },
    selAttempt() {
      if (!this.logDetail) return null;
      return this.logDetail.attempts.find(a => a.id === this.logDetail.sel) || this.logDetail.data;
    },
    sortedChannels() {
      // 优先级（大在前）→ id（小在前）
      return [...this.channels].sort((a, b) => b.priority - a.priority || a.id - b.id);
    },
    fetchRows() {
      if (!this.fetchDlg) return [];
      const q = (this.fetchDlg.filter || '').trim().toLowerCase();
      if (!q) return this.fetchDlg.rows;
      return this.fetchDlg.rows.filter(r => r.name.toLowerCase().includes(q));
    },
    existingRows() {
      return this.fetchRows.filter(r => r.category === 'existing');
    },
    newRows() {
      return this.fetchRows.filter(r => r.category === 'new');
    },
    allFetchChecked() {
      return this.newRows.length > 0 && this.newRows.every(r => r.checked);
    },
    mappingModelGroups() {
      const groups = [];
      const byChannel = new Map();
      for (const m of this.models) {
        const key = m.channel_name || '-';
        if (!byChannel.has(key)) {
          const g = { channel: key, items: [] };
          byChannel.set(key, g);
          groups.push(g);
        }
        byChannel.get(key).items.push(m);
      }
      return groups;
    },
  },
  methods: {
    // ---- 全局提示（Toast） ----
    toast(msg, type = 'error') {
      const id = Date.now() + Math.random();
      this.toasts.push({ id, msg, type });
    },
    closeToast(id) {
      this.toasts = this.toasts.filter(t => t.id !== id);
    },

    async guard(fn) {
      try {
        await fn();
      } catch (e) {
        if (e.status === 401) { this.authed = false; return; }
        this.toast(e.message, 'error');
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
      this.sidebarOpen = false;
      if (k === 'logs') this.loadLogs();
    },
    async loadAll() {
      await this.guard(async () => {
        const [ch, m, mp, tk] = await Promise.all([
          api('/channels'), api('/models'), api('/mappings'), api('/tokens'),
        ]);
        this.channels = ch.data || [];
        this.models = m.data || [];
        this.mappings = mp.data || [];
        this.tokens = tk.data || [];
        await this.loadLogs();
      });
    },
    async loadChannels() { await this.guard(async () => { this.channels = (await api('/channels')).data || []; }); },
    async loadModels() { await this.guard(async () => { this.models = (await api('/models')).data || []; }); },
    async loadMappings() { await this.guard(async () => { this.mappings = (await api('/mappings')).data || []; }); },
    channelModels(c) {
      return this.models.filter(m => m.channel_id === c.id);
    },
    showChannelModels(c) {
      const models = this.channelModels(c).map(m => ({
        name: m.name,
        bound: (m.mappings || []).join(', ') || '未绑定',
      }));
      this.chModelsDlg = { channel: c.name, models };
    },
    showMappingModels(m) {
      this.mappingModelsDlg = { name: m.name, models: m.bound_models || [] };
    },
    toggleFetchAll(e) {
      const v = e.target.checked;
      this.newRows.forEach(r => { r.checked = v; });
    },
    async loadTokens() { await this.guard(async () => { this.tokens = (await api('/tokens')).data || []; }); },
    async loadLogs() {
      await this.guard(async () => {
        const q = new URLSearchParams({ page: this.logs.page, size: this.logs.size });
        if (this.logFilter.model) q.set('model', this.logFilter.model);
        if (this.logFilter.status) q.set('status', this.logFilter.status);
        if (this.logFilter.token_id) q.set('token_id', this.logFilter.token_id);
        if (this.logFilter.channel_id) q.set('channel_id', this.logFilter.channel_id);
        if (this.logFilter.start_time) q.set('start_time', new Date(this.logFilter.start_time).toISOString());
        if (this.logFilter.end_time) q.set('end_time', new Date(this.logFilter.end_time).toISOString());
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
        };
      } else {
        this.chForm = { name: '', base_url: '', api_key: '', priority: 0, enabled: true, test_model: '' };
      }
    },
    async saveChannel() {
      await this.guard(async () => {
        const f = this.chForm;
        const body = {
          name: f.name, base_url: f.base_url, api_key: f.api_key,
          priority: f.priority, enabled: f.enabled, test_model: f.test_model,
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
        await Promise.all([this.loadChannels(), this.loadModels()]);
      });
    },
    async toggleChannel(c) {
      await this.guard(async () => {
        await api('/channels/' + c.id + '/toggle', { method: 'POST', body: { enabled: !c.enabled } });
        await this.loadChannels();
      });
    },
    async fetchModels(c) {
      await this.guard(async () => {
        const v = await api('/channels/' + c.id + '/fetch-models', { method: 'POST' });
        const rows = (v.data || []).map(r => ({
          checked: !r.exists,
          category: r.exists ? 'existing' : 'new',
          name: r.name,
          context_length: r.context_length,
          max_output_tokens: r.max_output_tokens,
        }));
        this.fetchDlg = { channelID: c.id, channelName: c.name, filter: '', rows };
      });
    },
    async saveFetch() {
      await this.guard(async () => {
        const names = this.fetchDlg.rows
          .filter(r => r.checked && r.category === 'new')
          .map(r => r.name);
        if (!names.length) { this.fetchDlg = null; return; }
        await api('/channels/' + this.fetchDlg.channelID + '/models', { method: 'POST', body: { names } });
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

    // ---- 模型（渠道下的模型实体） ----
    openModelForm(m) {
      if (m) {
        this.modelForm = { id: m.id, channel_id: m.channel_id, name: m.name };
      } else {
        this.modelForm = { channel_id: this.channels.length ? this.channels[0].id : null, name: '' };
      }
    },
    async saveModel() {
      await this.guard(async () => {
        const f = this.modelForm;
        if (f.id) {
          await api('/models/' + f.id, { method: 'PUT', body: { name: f.name } });
        } else {
          await api('/models', { method: 'POST', body: { channel_id: f.channel_id, name: f.name } });
        }
        this.modelForm = null;
        await this.loadModels();
      });
    },
    async delModel(m) {
      if (!confirm('删除模型「' + m.name + '」？与标准名的绑定关系会一并移除。')) return;
      await this.guard(async () => {
        await api('/models/' + m.id, { method: 'DELETE' });
        await Promise.all([this.loadModels(), this.loadMappings()]);
      });
    },

    // ---- 模型映射（标准名） ----
    openMappingForm(m) {
      if (m) {
        this.mappingForm = {
          id: m.id, name: m.name,
          context_length: m.context_length, max_output_tokens: m.max_output_tokens,
          model_ids: (m.bound_models || []).map(x => x.id),
        };
      } else {
        this.mappingForm = { name: '', context_length: null, max_output_tokens: null, model_ids: [] };
      }
    },
    async saveMapping() {
      await this.guard(async () => {
        const f = this.mappingForm;
        const body = {
          name: f.name,
          context_length: f.context_length || null,
          max_output_tokens: f.max_output_tokens || null,
          model_ids: f.model_ids,
        };
        if (f.id) await api('/mappings/' + f.id, { method: 'PUT', body });
        else await api('/mappings', { method: 'POST', body });
        this.mappingForm = null;
        await Promise.all([this.loadMappings(), this.loadModels()]);
      });
    },
    async delMapping(m) {
      if (!confirm('删除映射「' + m.name + '」？与模型的绑定关系会一并移除。')) return;
      await this.guard(async () => {
        await api('/mappings/' + m.id, { method: 'DELETE' });
        await Promise.all([this.loadMappings(), this.loadModels()]);
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
      this.copyText(this.createdToken.token);
    },
    async revealToken(t) {
      await this.guard(async () => {
        const v = await api('/tokens/' + t.id);
        await this.copyText(v.token);
      });
    },
    async copyText(text) {
      try {
        await navigator.clipboard.writeText(text);
        this.toast('已复制到剪贴板', 'success');
      } catch (e) {
        this.toast('复制失败：' + e.message, 'error');
      }
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
    fmtTokens(l) {
      let s = String(l.prompt_tokens);
      if (l.prompt_cache_hit_tokens) s += `(${l.prompt_cache_hit_tokens})`;
      return s + '+' + l.completion_tokens;
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
