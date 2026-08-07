<script setup>
import { ref } from 'vue'
import { authApi, setToken } from '../api'

const emit = defineEmits(['login'])
const password = ref('')
const error = ref('')
const busy = ref(false)

async function submit() {
  if (!password.value || busy.value) return
  busy.value = true
  error.value = ''
  try {
    const data = await authApi.login(password.value)
    setToken(data.token)
    emit('login')
  } catch (e) {
    error.value = e.message
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <div class="login-wrap">
    <form class="card login-box" @submit.prevent="submit">
      <h1>STRMhub 管理台</h1>
      <p class="muted">请输入管理员密码(环境变量 STRMHUB_ADMIN_PASSWORD)</p>
      <input v-model="password" type="password" placeholder="密码" autofocus />
      <div v-if="error" class="msg err">{{ error }}</div>
      <button class="primary" type="submit" :disabled="busy">
        {{ busy ? '登录中...' : '登录' }}
      </button>
    </form>
  </div>
</template>

<style scoped>
.login-wrap { min-height: 100vh; display: flex; align-items: center; justify-content: center; }
.login-box { width: 340px; display: flex; flex-direction: column; gap: 10px; }
</style>
