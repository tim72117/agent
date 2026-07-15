import { createApp } from 'vue'
import { createVuetify } from 'vuetify'
import { createRouter, createWebHistory } from 'vue-router'
import * as components from 'vuetify/components'
import 'vuetify/styles'

import moment from 'moment'
import Census from './components/Census.vue'
import CensusMenu from './components/Menu.vue'
import CensusFooter from './components/CensusFooter.vue'

const routes = [
    { path: '/analysis/:doc/open', component: Census, name: 'census' },
    { path: '/analysis/:doc/menu', component: CensusMenu, name: 'menu' },
]

const router = createRouter({
    history: createWebHistory(),
    routes,
})

const vuetify = createVuetify({
    components: {
        ...components,
    },
    theme: typeof theme === 'undefined' ? {} : theme,
})

const app = createApp({
    components: {
        CensusFooter,
    },
})

app.config.globalProperties.$moment = {
    dateFormat(date) {
        return moment(date).format('yyyy-MM-DD')
    },
}

app.use(router)
app.use(vuetify)

app.mount('#app')
