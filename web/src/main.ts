import ElementPlus from 'element-plus'
import { createApp } from 'vue'
import 'element-plus/dist/index.css'
import { createRouter, createWebHashHistory } from 'vue-router'

import App from './App.vue'
import { isAuthed } from './api/client'
import Dashboard from './views/Dashboard.vue'
import Login from './views/Login.vue'
import Nodes from './views/Nodes.vue'
import Routes from './views/Routes.vue'

const router = createRouter({
  history: createWebHashHistory(),
  routes: [
    { path: '/login', component: Login },
    { path: '/', component: Dashboard, meta: { auth: true } },
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

createApp(App).use(router).use(ElementPlus).mount('#app')
