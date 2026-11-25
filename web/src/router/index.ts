import { createRouter, createWebHistory } from 'vue-router'
import { useAuthStore } from '../stores/auth'

const router = createRouter({
  history: createWebHistory(),
  routes: [
    { path: '/setup', name: 'setup', component: () => import('../views/SetupView.vue'), meta: { public: true } },
    { path: '/login', name: 'login', component: () => import('../views/LoginView.vue'), meta: { public: true } },
    {
      path: '/',
      component: () => import('../layouts/AppLayout.vue'),
      children: [
        { path: '', name: 'home', component: () => import('../views/HomeView.vue') },
        { path: 'library/:id', name: 'library', component: () => import('../views/LibraryView.vue') },
        { path: 'item/:id', name: 'item', component: () => import('../views/ItemDetailView.vue') },
        { path: 'play/:id', name: 'play', component: () => import('../views/PlayView.vue') },
        { path: 'admin', name: 'admin', component: () => import('../views/AdminView.vue'), meta: { admin: true } },
      ],
    },
  ],
})

router.beforeEach(async (to) => {
  const auth = useAuthStore()
  if (!auth.bootstrapped) {
    await auth.bootstrap()
  }
  if (!auth.initialized) {
    if (to.name !== 'setup') return { name: 'setup' }
    return true
  }
  if (to.name === 'setup') {
    return auth.isAuthenticated ? { name: 'home' } : { name: 'login' }
  }
  if (!to.meta.public && !auth.isAuthenticated) {
    return { name: 'login', query: { redirect: to.fullPath } }
  }
  if (to.meta.admin && !auth.isAdmin) {
    return { name: 'home' }
  }
  return true
})

export default router
