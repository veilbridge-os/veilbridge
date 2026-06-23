<script setup lang="ts">
import { ElMessage } from 'element-plus'
import { ref } from 'vue'
import { useRouter } from 'vue-router'
import { api } from '../api/client'

const router = useRouter()
const password = ref('')
const loading = ref(false)

async function submit() {
  if (!password.value) return
  loading.value = true
  try {
    await api.login(password.value)
    router.push('/')
  } catch (e) {
    ElMessage.error('Login failed: ' + (e as Error).message)
  } finally {
    loading.value = false
  }
}
</script>

<template>
  <div class="vb-login">
    <el-card class="vb-login-card">
      <h2 style="margin-top: 0">VeilBridge</h2>
      <el-form @submit.prevent="submit">
        <el-form-item>
          <el-input
            v-model="password"
            type="password"
            placeholder="Admin password"
            show-password
            @keyup.enter="submit"
          />
        </el-form-item>
        <el-button type="primary" :loading="loading" style="width: 100%" @click="submit">
          Sign in
        </el-button>
      </el-form>
    </el-card>
  </div>
</template>

<style scoped>
.vb-login {
  display: flex;
  justify-content: center;
  align-items: center;
  height: 70vh;
}
.vb-login-card {
  width: 320px;
}
</style>
