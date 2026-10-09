import { createApp } from 'vue'
import dayjs from 'dayjs'
import 'dayjs/locale/zh-cn'
import App from './App.vue'
import './style.css'

// ant-design-vue 4 为 CSS-in-JS：组件样式随渲染自动生成，无需手动引 css

dayjs.locale('zh-cn')

createApp(App).mount('#app')
