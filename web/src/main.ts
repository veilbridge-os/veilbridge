import { createApp } from 'vue'
// Element Plus dark variables. They cost ~2 kB gzipped and are inert until
// <html class="dark"> appears, so there is no reason to load them lazily.
import 'element-plus/theme-chalk/dark/css-vars.css'
// Our own tokens come AFTER Element Plus's, or its defaults win: the panel
// rendered in Times with stock blue buttons until this file existed (M2.7).
import '@/styles/tokens.css'
import '@/stores/theme'
import { createRouter, createWebHashHistory } from 'vue-router'
import { isAuthed } from '@/api/client'
import { i18n } from '@/i18n'
import Dashboard from '@/views/Dashboard.vue'
import Login from '@/views/Login.vue'
import Nodes from '@/views/Nodes.vue'
import Routes from '@/views/Routes.vue'
import Wan from '@/views/Wan.vue'
import App from './App.vue'

const router = createRouter({
  history: createWebHashHistory(),
  routes: [
    { path: '/login', component: Login },
    { path: '/', component: Dashboard, meta: { auth: true } },
    { path: '/internet', component: Wan, meta: { auth: true } },
    { path: '/nodes', component: Nodes, meta: { auth: true } },
    { path: '/routes', component: Routes, meta: { auth: true } },
  ],
})

// Guard: protected routes require a token; bounce to /login otherwise.
router.beforeEach((to) => {
  if (to.meta.auth && !isAuthed()) return '/login'
  if (to.path === '/login' && isAuthed()) return '/'
  return true
})

createApp(App).use(router).use(i18n).mount('#app')
