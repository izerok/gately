<template>
  <div>
    <PageHeader :title="t('plugin.title')" :desc="t('plugin.desc')" :icon="Puzzle">
      <el-button type="primary" :icon="Plus" @click="openEditor()">{{ t('plugin.add') }}</el-button>
    </PageHeader>

    <el-alert v-if="createdSecret" :title="t('plugin.secretOnce')" type="warning" :closable="false" class="secret-alert">
      <span class="mono">{{ createdSecret }}</span>
    </el-alert>

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
      <el-table-column :label="t('common.actions')" width="300">
        <template #default="{ row }">
          <el-button text size="small" :disabled="row.status.state !== 'running'" @click="openPage(row)">{{ t('plugin.open') }}</el-button>
          <el-button v-if="row.status.state !== 'running'" text size="small" :icon="Play" :disabled="!row.enabled" @click="act(row, 'start')">{{ t('plugin.start') }}</el-button>
          <el-button v-else text size="small" :icon="Square" @click="act(row, 'stop')">{{ t('plugin.stop') }}</el-button>
          <el-button text size="small" :icon="RotateCw" :disabled="!row.enabled" @click="act(row, 'restart')">{{ t('plugin.restart') }}</el-button>
          <el-button text size="small" :icon="Pencil" @click="openEditor(row)">{{ t('common.edit') }}</el-button>
          <el-popconfirm :title="t('plugin.confirmDelete')" @confirm="remove(row)">
            <template #reference>
              <el-button text size="small" type="danger" :icon="Trash2">{{ t('common.delete') }}</el-button>
            </template>
          </el-popconfirm>
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
  </div>
</template>

<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref } from 'vue'
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
  status: PluginStatus
}

const plugins = ref<PluginView[]>([])
const editorVisible = ref(false)
const saving = ref(false)
const createdSecret = ref('')
const argsText = ref('')
const envText = ref('')
const scopesText = ref('')
const editing = ref<PluginView>({
  id: '', name: '', version: '', description: '', icon: '', kind: 'command',
  command: '', args: [], env: {}, workDir: '', url: '', port: 0,
  healthPath: '/health', enabled: true, scopes: [],
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

async function remove(row: PluginView) {
  try {
    await api.del(`/api/plugin/plugins/${row.id}`)
    await refresh()
  } catch (e) {
    ElMessage.error(String(e))
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
      healthPath: '/health', enabled: true, scopes: [],
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
onMounted(() => {
  refresh()
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
.secret-alert {
  margin-bottom: 16px;
}
.mono {
  font-family: monospace;
  word-break: break-all;
}
</style>
