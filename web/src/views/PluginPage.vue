<template>
  <div class="plugin-page">
    <PageHeader :title="name" :desc="t('plugin.pageDesc')" :icon="Puzzle">
      <el-button :icon="ExternalLink" @click="openNewTab">{{ t('plugin.openNewTab') }}</el-button>
    </PageHeader>
    <iframe v-if="id" :src="`/api/plugin/pages/${id}/`" class="frame" />
    <el-empty v-else :description="t('plugin.notFound')" />
  </div>
</template>

<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useRoute } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { ExternalLink, Puzzle } from 'lucide-vue-next'
import { api } from '../api'
import PageHeader from '../components/PageHeader.vue'

const { t } = useI18n()
const route = useRoute()
const id = route.params.id as string
const name = ref(id)

function openNewTab() {
  window.open(`/api/plugin/pages/${id}/`, '_blank')
}

onMounted(async () => {
  try {
    const p = await api.get<{ name: string }>(`/api/plugin/plugins/${id}`)
    name.value = p.name || id
  } catch {
    // 取不到详情时仍以 ID 为标题展示 iframe，错误由反代返回
  }
})
</script>

<style scoped>
.plugin-page {
  display: flex;
  flex-direction: column;
  height: 100%;
}
.frame {
  flex: 1;
  min-height: 600px;
  width: 100%;
  border: 1px solid var(--el-border-color-lighter);
  border-radius: 8px;
}
</style>
