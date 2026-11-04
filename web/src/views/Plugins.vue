<template>
  <div>
    <PageHeader :title="t('plugin.title')" :desc="t('plugin.desc')" :icon="Puzzle">
      <el-button type="primary" :icon="Plus" @click="openEditor()">{{ t('plugin.add') }}</el-button>
    </PageHeader>

    <el-alert v-if="createdSecret" :title="t('plugin.secretOnce')" type="warning" :closable="false" class="secret-alert">
      <span class="mono">{{ createdSecret }}</span>
    </el-alert>

    <el-card class="market-card">
      <template #header>
        <div class="card-header">
          <span>{{ t('plugin.market') }}</span>
          <span>
            <el-button size="small" @click="openSourceEditor()">{{ t('plugin.addSource') }}</el-button>
            <el-button size="small" @click="localVisible = true">{{ t('plugin.installLocal') }}</el-button>
            <el-button size="small" @click="triggerUpload">{{ t('plugin.installUpload') }}</el-button>
            <input ref="uploadInput" type="file" accept=".zip" style="display:none" @change="doUpload" />
          </span>
        </div>
      </template>
      <el-form inline>
        <el-form-item :label="t('plugin.source')">
          <el-select v-model="selectedSource" style="width: 280px" @change="refreshIndex">
            <el-option v-for="s in sources" :key="s.id" :value="s.id" :label="sourceLabel(s)" />
          </el-select>
        </el-form-item>
        <el-form-item>
          <el-button :icon="RotateCw" :loading="indexLoading" @click="refreshIndex">{{ t('plugin.refreshIndex') }}</el-button>
        </el-form-item>
        <el-form-item>
          <el-button text size="small" :disabled="!currentSource" @click="openSourceEditor(currentSource)">{{ t('common.edit') }}</el-button>
          <el-button v-if="currentSource && !(currentSource.id === 'official' && !currentSource.url && !currentSource.dir)" text size="small" type="danger" @click="removeSource">{{ t('common.delete') }}</el-button>
        </el-form-item>
      </el-form>
      <el-alert v-if="indexError" :title="indexError" type="error" :closable="false" class="index-alert" />
      <el-alert v-if="currentSource && !currentSource.url && !currentSource.dir" :title="t('plugin.officialEmpty')" type="info" :closable="false" class="index-alert" />
      <el-table :data="available" stripe style="width: 100%" :empty-text="t('plugin.marketEmpty')">
        <el-table-column prop="name" :label="t('common.name')" min-width="140">
          <template #default="{ row }">
            <div>{{ row.icon ? `${row.icon} ` : '' }}{{ row.name }}</div>
            <div v-if="row.description" class="sub">{{ row.description }}</div>
          </template>
        </el-table-column>
        <el-table-column prop="id" label="ID" width="120" class-name="mono" />
        <el-table-column prop="version" :label="t('plugin.version')" width="110" />
        <el-table-column :label="t('plugin.installedVersion')" width="130">
          <template #default="{ row }">{{ row.installedVersion || '-' }}</template>
        </el-table-column>
        <el-table-column :label="t('common.actions')" width="220">
          <template #default="{ row }">
            <el-button v-if="row.upgradable" text size="small" type="primary" @click="upgradeFromRegistry(row)">{{ t('plugin.upgrade') }}</el-button>
            <el-button v-else-if="row.installedVersion" text size="small" disabled>{{ t('plugin.installed') }}</el-button>
            <el-button v-else text size="small" type="primary" @click="installFromRegistry(row)">{{ t('plugin.install') }}</el-button>
          </template>
        </el-table-column>
      </el-table>
    </el-card>

    <el-table :data="plugins" stripe style="width: 100%" :empty-text="t('plugin.empty')">
      <el-table-column prop="name" :label="t('common.name')" min-width="140">
        <template #default="{ row }">
          <div>{{ row.icon ? `${row.icon} ` : '' }}{{ row.name }}</div>
          <div v-if="row.description" class="sub">{{ row.description }}</div>
        </template>
      </el-table-column>
      <el-table-column prop="id" label="ID" width="120" class-name="mono" />
      <el-table-column prop="kind" :label="t('plugin.kind')" width="100">
        <template #default="{ row }">{{ row.kind === 'command' ? t('plugin.kindCommand') : t('plugin.kindUrl') }}</template>
      </el-table-column>
      <el-table-column :label="t('plugin.status')" width="110">
        <template #default="{ row }"><StatusBadge v-bind="statusOf(row)" /></template>
      </el-table-column>
      <el-table-column :label="t('plugin.target')" min-width="180">
        <template #default="{ row }"><span class="mono">{{ row.status.target || '-' }}</span></template>
      </el-table-column>
      <el-table-column :label="t('plugin.source')" width="130">
        <template #default="{ row }">{{ sourceTag(row) }}</template>
      </el-table-column>
      <el-table-column :label="t('common.actions')" width="430">
        <template #default="{ row }">
          <el-button text size="small" :disabled="row.status.state !== 'running'" @click="openPage(row)">{{ t('plugin.open') }}</el-button>
          <el-button v-if="row.status.state !== 'running'" text size="small" :icon="Play" :disabled="!row.enabled" @click="act(row, 'start')">{{ t('plugin.start') }}</el-button>
          <el-button v-else text size="small" :icon="Square" @click="act(row, 'stop')">{{ t('plugin.stop') }}</el-button>
          <el-button text size="small" :icon="RotateCw" :disabled="!row.enabled" @click="act(row, 'restart')">{{ t('plugin.restart') }}</el-button>
          <el-button text size="small" @click="openOptions(row)">{{ t('plugin.options') }}</el-button>
          <el-button text size="small" :icon="Pencil" @click="openEditor(row)">{{ t('common.edit') }}</el-button>
          <el-button text size="small" type="danger" :icon="Trash2" @click="openUninstall(row)">{{ t('common.delete') }}</el-button>
        </template>
      </el-table-column>
    </el-table>

    <LogCard url="/api/plugin/logs" clearable />

    <el-dialog v-model="editorVisible" :title="editing.id ? t('plugin.editPlugin') : t('plugin.addPlugin')" width="640px">
      <el-form :model="editing" label-width="130px">
        <el-form-item :label="t('plugin.id')" v-if="!editing.id"><el-input v-model="editing.id" placeholder="navpage" /></el-form-item>
        <el-form-item :label="t('plugin.id')" v-else><el-input v-model="editing.id" disabled /></el-form-item>
        <el-form-item :label="t('common.name')"><el-input v-model="editing.name" /></el-form-item>
        <el-form-item :label="t('plugin.icon')"><el-input v-model="editing.icon" placeholder="🧭" /></el-form-item>
        <el-form-item :label="t('plugin.version')"><el-input v-model="editing.version" placeholder="0.1.0" /></el-form-item>
        <el-form-item :label="t('plugin.description')"><el-input v-model="editing.description" type="textarea" :rows="2" /></el-form-item>
        <el-form-item :label="t('plugin.kind')">
          <el-select v-model="editing.kind">
            <el-option value="command" :label="t('plugin.kindCommand')" />
            <el-option value="url" :label="t('plugin.kindUrl')" />
          </el-select>
        </el-form-item>
        <el-form-item v-if="editing.kind === 'command'" :label="t('plugin.command')"><el-input v-model="editing.command" placeholder="/usr/local/bin/navpage" /></el-form-item>
        <el-form-item v-if="editing.kind === 'command'" :label="t('plugin.args')"><el-input v-model="argsText" type="textarea" :rows="2" :placeholder="t('plugin.argsPlaceholder')" /></el-form-item>
        <el-form-item v-if="editing.kind === 'command'" :label="t('plugin.env')"><el-input v-model="envText" type="textarea" :rows="2" :placeholder="t('plugin.envPlaceholder')" /></el-form-item>
        <el-form-item v-if="editing.kind === 'command'" :label="t('plugin.workDir')"><el-input v-model="editing.workDir" /></el-form-item>
        <el-form-item v-if="editing.kind === 'url'" :label="t('plugin.url')"><el-input v-model="editing.url" placeholder="http://127.0.0.1:8080" /></el-form-item>
        <el-form-item v-if="editing.kind === 'command'" :label="t('plugin.port')"><el-input-number v-model="editing.port" :min="0" :max="65535" /></el-form-item>
        <el-form-item :label="t('plugin.healthPath')"><el-input v-model="editing.healthPath" placeholder="/health" /></el-form-item>
        <el-form-item :label="t('plugin.scopes')"><el-input v-model="scopesText" :placeholder="t('plugin.scopesPlaceholder')" /></el-form-item>
        <el-form-item :label="t('plugin.enabled')"><el-switch v-model="editing.enabled" /></el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="editorVisible = false">{{ t('common.cancel') }}</el-button>
        <el-button type="primary" :loading="saving" @click="save">{{ t('common.save') }}</el-button>
      </template>
    </el-dialog>

    <el-dialog v-model="sourceVisible" :title="editingSource.id ? t('plugin.editSource') : t('plugin.addSource')" width="560px">
      <el-form :model="editingSource" label-width="110px">
        <el-form-item :label="t('common.name')"><el-input v-model="editingSource.name" /></el-form-item>
        <el-form-item :label="t('plugin.sourceKind')">
          <el-select v-model="editingSource.kind">
            <el-option value="official" :label="t('plugin.official')" />
            <el-option value="community" :label="t('plugin.community')" />
          </el-select>
        </el-form-item>
        <el-form-item :label="t('plugin.indexUrl')"><el-input v-model="editingSource.url" placeholder="https://example.com/index.json" /></el-form-item>
        <el-form-item :label="t('plugin.localDir')"><el-input v-model="editingSource.dir" placeholder="/etc/gately/registry" /></el-form-item>
        <div class="sub">{{ t('plugin.sourceHint') }}</div>
      </el-form>
      <template #footer>
        <el-button @click="sourceVisible = false">{{ t('common.cancel') }}</el-button>
        <el-button type="primary" :loading="saving" @click="saveSource">{{ t('common.save') }}</el-button>
      </template>
    </el-dialog>

    <el-dialog v-model="localVisible" :title="t('plugin.installLocal')" width="560px">
      <el-form label-width="110px">
        <el-form-item :label="t('plugin.localDir')"><el-input v-model="localDir" placeholder="/path/to/plugin-package" /></el-form-item>
        <div class="sub">{{ t('plugin.localHint') }}</div>
      </el-form>
      <template #footer>
        <el-button @click="localVisible = false">{{ t('common.cancel') }}</el-button>
        <el-button type="primary" :loading="saving" @click="installLocal">{{ t('plugin.install') }}</el-button>
      </template>
    </el-dialog>

    <el-dialog v-model="uninstallVisible" :title="t('common.delete')" width="480px">
      <p>{{ t('plugin.confirmDelete') }}</p>
      <el-checkbox v-if="uninstallTarget?.installDir" v-model="deleteFiles">{{ t('plugin.deleteFiles') }}</el-checkbox>
      <div v-if="uninstallTarget?.installDir" class="sub mono">{{ uninstallTarget.installDir }}</div>
      <template #footer>
        <el-button @click="uninstallVisible = false">{{ t('common.cancel') }}</el-button>
        <el-button type="danger" :loading="saving" @click="confirmUninstall">{{ t('common.delete') }}</el-button>
      </template>
    </el-dialog>

    <el-dialog v-model="optionsVisible" :title="t('plugin.options')" width="640px">
      <el-alert v-if="optionsTarget?.kind === 'url'" :title="t('plugin.optionsUrlNote')" type="info" :closable="false" class="index-alert" />
      <el-input v-model="optionsText" type="textarea" :rows="12" spellcheck="false" class="mono" />
      <div class="sub">{{ t('plugin.optionsHint') }}</div>
      <template #footer>
        <el-button @click="optionsVisible = false">{{ t('common.cancel') }}</el-button>
        <el-button type="primary" :loading="saving" @click="saveOptions">{{ t('common.save') }}</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { ElMessage } from 'element-plus'
import { Pencil, Play, Plus, Puzzle, RotateCw, Square, Trash2 } from 'lucide-vue-next'
import { api } from '../api'
import PageHeader from '../components/PageHeader.vue'
import LogCard from '../components/LogCard.vue'
import StatusBadge, { type StatusType } from '../components/StatusBadge.vue'

const { t } = useI18n()
const router = useRouter()

interface PluginStatus {
  state: string
  pid: number
  port: number
  target: string
  lastHealth: string
  lastError: string
  restarts: number
  startedAt: string
}

interface PluginView {
  id: string
  name: string
  version: string
  description: string
  icon: string
  kind: string
  command: string
  args: string[]
  env: Record<string, string>
  workDir: string
  url: string
  port: number
  healthPath: string
  enabled: boolean
  scopes: string[]
  options: Record<string, unknown>
  source: string
  installDir: string
  capabilities: string[]
  status: PluginStatus
}

interface RegistrySource {
  id: string
  name: string
  kind: string
  url: string
  dir: string
}

interface RegistryEntryView {
  id: string
  name: string
  version: string
  description: string
  icon: string
  kind: string
  downloadUrl: string
  installedVersion: string
  upgradable: boolean
}

const plugins = ref<PluginView[]>([])
const sources = ref<RegistrySource[]>([])
const selectedSource = ref('')
const available = ref<RegistryEntryView[]>([])
const indexLoading = ref(false)
const indexError = ref('')
const sourceVisible = ref(false)
const localVisible = ref(false)
const localDir = ref('')
const uninstallVisible = ref(false)
const uninstallTarget = ref<PluginView | null>(null)
const deleteFiles = ref(false)
const optionsVisible = ref(false)
const optionsTarget = ref<PluginView | null>(null)
const optionsText = ref('')
const uploadInput = ref<HTMLInputElement | null>(null)
const editingSource = ref<RegistrySource>({ id: '', name: '', kind: 'community', url: '', dir: '' })

const currentSource = computed(() => sources.value.find((s) => s.id === selectedSource.value))
const editorVisible = ref(false)
const saving = ref(false)
const createdSecret = ref('')
const argsText = ref('')
const envText = ref('')
const scopesText = ref('')
const editing = ref<PluginView>({
  id: '', name: '', version: '', description: '', icon: '', kind: 'command',
  command: '', args: [], env: {}, workDir: '', url: '', port: 0,
  healthPath: '/health', enabled: true, scopes: [], options: {}, source: '', installDir: '', capabilities: [],
  status: { state: 'stopped', pid: 0, port: 0, target: '', lastHealth: '', lastError: '', restarts: 0, startedAt: '' },
})

function statusOf(row: PluginView): { type: StatusType, text: string } {
  if (row.status.state === 'running') return { type: 'ok', text: t('plugin.stateRunning') }
  if (row.status.state === 'error') return { type: 'error', text: row.status.lastError || t('plugin.stateError') }
  if (row.status.state === 'starting') return { type: 'info', text: t('plugin.stateStarting') }
  return { type: 'info', text: t('plugin.stateStopped') }
}

async function refresh() {
  try {
    plugins.value = await api.get<PluginView[]>('/api/plugin/plugins')
  } catch {
    // 管理页轮询失败时保持旧数据，不刷屏报错
  }
}

function openPage(row: PluginView) {
  router.push({ name: 'plugin-page', params: { id: row.id } })
}

async function act(row: PluginView, action: string) {
  try {
    await api.post(`/api/plugin/plugins/${row.id}/${action}`)
    await refresh()
  } catch (e) {
    ElMessage.error(String(e))
  }
}

function sourceLabel(s: RegistrySource): string {
  return `${s.name} (${s.kind === 'official' ? t('plugin.official') : t('plugin.community')})`
}

function sourceTag(row: PluginView): string {
  if (row.source.startsWith('registry:')) {
    const src = sources.value.find((s) => s.id === row.source.slice('registry:'.length))
    const kind = src?.kind === 'official' ? t('plugin.official') : t('plugin.community')
    return `${kind} / ${row.source.slice('registry:'.length)}`
  }
  if (row.source === 'local') return t('plugin.sourceLocal')
  if (row.source === 'upload') return t('plugin.sourceUpload')
  return t('plugin.sourceManual')
}

async function refreshSources() {
  try {
    sources.value = await api.get<RegistrySource[]>('/api/plugin/registry/sources')
    if (!sources.value.some((s) => s.id === selectedSource.value)) {
      selectedSource.value = sources.value[0]?.id ?? ''
    }
  } catch (e) {
    ElMessage.error(String(e))
  }
}

// 索引只在管理界面打开 / 手动刷新时拉取，不进 10 秒轮询。
async function refreshIndex() {
  if (!selectedSource.value) {
    available.value = []
    return
  }
  indexLoading.value = true
  indexError.value = ''
  try {
    const res = await api.get<{ plugins: RegistryEntryView[] }>(`/api/plugin/registry/index?source=${encodeURIComponent(selectedSource.value)}`)
    available.value = res.plugins ?? []
  } catch (e) {
    available.value = []
    indexError.value = String(e)
  } finally {
    indexLoading.value = false
  }
}

function openSourceEditor(row?: RegistrySource) {
  editingSource.value = row
    ? JSON.parse(JSON.stringify(row)) as RegistrySource
    : { id: '', name: '', kind: 'community', url: '', dir: '' }
  sourceVisible.value = true
}

async function saveSource() {
  saving.value = true
  try {
    if (editingSource.value.id) {
      await api.put(`/api/plugin/registry/sources/${editingSource.value.id}`, editingSource.value)
    } else {
      const res = await api.post<RegistrySource>('/api/plugin/registry/sources', editingSource.value)
      selectedSource.value = res.id
    }
    sourceVisible.value = false
    await refreshSources()
    await refreshIndex()
  } catch (e) {
    ElMessage.error(String(e))
  } finally {
    saving.value = false
  }
}

async function removeSource() {
  if (!currentSource.value) return
  try {
    await api.del(`/api/plugin/registry/sources/${currentSource.value.id}`)
    selectedSource.value = ''
    await refreshSources()
    await refreshIndex()
  } catch (e) {
    ElMessage.error(String(e))
  }
}

async function installFromRegistry(row: RegistryEntryView) {
  try {
    const res = await api.post<{ plugin: PluginView, secret: string }>('/api/plugin/install/registry', { sourceId: selectedSource.value, pluginId: row.id })
    createdSecret.value = res.secret
    await refresh()
    await refreshIndex()
  } catch (e) {
    ElMessage.error(String(e))
  }
}

async function upgradeFromRegistry(row: RegistryEntryView) {
  try {
    await api.post(`/api/plugin/plugins/${row.id}/upgrade`, { sourceId: selectedSource.value })
    await refresh()
    await refreshIndex()
  } catch (e) {
    ElMessage.error(String(e))
  }
}

async function installLocal() {
  saving.value = true
  try {
    const res = await api.post<{ plugin: PluginView, secret: string }>('/api/plugin/install/local', { dir: localDir.value.trim() })
    createdSecret.value = res.secret
    localVisible.value = false
    localDir.value = ''
    await refresh()
  } catch (e) {
    ElMessage.error(String(e))
  } finally {
    saving.value = false
  }
}

function triggerUpload() {
  uploadInput.value?.click()
}

async function doUpload() {
  const file = uploadInput.value?.files?.[0]
  uploadInput.value!.value = ''
  if (!file) return
  const form = new FormData()
  form.append('file', file)
  try {
    const res = await api.post<{ plugin: PluginView, secret: string }>('/api/plugin/install/upload', form)
    createdSecret.value = res.secret
    await refresh()
  } catch (e) {
    ElMessage.error(String(e))
  }
}

function openUninstall(row: PluginView) {
  uninstallTarget.value = row
  deleteFiles.value = false
  uninstallVisible.value = true
}

async function confirmUninstall() {
  if (!uninstallTarget.value) return
  saving.value = true
  try {
    await api.del(`/api/plugin/plugins/${uninstallTarget.value.id}?deleteFiles=${deleteFiles.value ? '1' : '0'}`)
    uninstallVisible.value = false
    await refresh()
    await refreshIndex()
  } catch (e) {
    ElMessage.error(String(e))
  } finally {
    saving.value = false
  }
}

function openOptions(row: PluginView) {
  optionsTarget.value = row
  optionsText.value = JSON.stringify(row.options ?? {}, null, 2)
  optionsVisible.value = true
}

async function saveOptions() {
  if (!optionsTarget.value) return
  let parsed: Record<string, unknown>
  try {
    parsed = JSON.parse(optionsText.value) as Record<string, unknown>
    if (parsed === null || typeof parsed !== 'object' || Array.isArray(parsed)) throw new Error('not an object')
  } catch {
    ElMessage.error(t('plugin.optionsInvalid'))
    return
  }
  saving.value = true
  try {
    await api.put(`/api/plugin/plugins/${optionsTarget.value.id}/options`, { options: parsed })
    optionsVisible.value = false
    await refresh()
  } catch (e) {
    ElMessage.error(String(e))
  } finally {
    saving.value = false
  }
}

function openEditor(row?: PluginView) {
  createdSecret.value = ''
  if (row) {
    editing.value = JSON.parse(JSON.stringify(row)) as PluginView
  } else {
    editing.value = {
      id: '', name: '', version: '', description: '', icon: '', kind: 'command',
      command: '', args: [], env: {}, workDir: '', url: '', port: 0,
      healthPath: '/health', enabled: true, scopes: [], options: {}, source: '', installDir: '', capabilities: [],
      status: { state: 'stopped', pid: 0, port: 0, target: '', lastHealth: '', lastError: '', restarts: 0, startedAt: '' },
    }
  }
  argsText.value = (editing.value.args ?? []).join('\n')
  envText.value = Object.entries(editing.value.env ?? {}).map(([k, v]) => `${k}=${v}`).join('\n')
  scopesText.value = (editing.value.scopes ?? []).join(', ')
  editorVisible.value = true
}

async function save() {
  const body: Record<string, unknown> = { ...editing.value }
  delete body.status
  body.args = argsText.value.split('\n').map((s) => s.trim()).filter(Boolean)
  const env: Record<string, string> = {}
  envText.value.split('\n').forEach((line) => {
    const idx = line.indexOf('=')
    if (idx > 0) env[line.slice(0, idx).trim()] = line.slice(idx + 1)
  })
  body.env = env
  body.scopes = scopesText.value.split(',').map((s) => s.trim()).filter(Boolean)
  saving.value = true
  try {
    if (editing.value.id && plugins.value.some((p) => p.id === editing.value.id)) {
      await api.put(`/api/plugin/plugins/${editing.value.id}`, body)
    } else {
      const res = await api.post<{ plugin: PluginView, secret: string }>('/api/plugin/plugins', body)
      createdSecret.value = res.secret
    }
    editorVisible.value = false
    await refresh()
  } catch (e) {
    ElMessage.error(String(e))
  } finally {
    saving.value = false
  }
}

let timer: ReturnType<typeof setInterval> | undefined
onMounted(async () => {
  await refresh()
  await refreshSources()
  await refreshIndex()
  // 轮询只刷新已安装插件状态，不拉索引（版本检查只在打开 / 手动刷新时做）
  timer = setInterval(refresh, 10000)
})
onBeforeUnmount(() => {
  if (timer) clearInterval(timer)
})
</script>

<style scoped>
.sub {
  color: var(--el-text-color-secondary);
  font-size: 12px;
}
.market-card {
  margin-bottom: 16px;
}
.card-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
}
.index-alert {
  margin-bottom: 12px;
}
.secret-alert {
  margin-bottom: 16px;
}
.mono {
  font-family: monospace;
  word-break: break-all;
}
</style>
