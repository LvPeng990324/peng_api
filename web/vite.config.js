import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'
import Components from 'unplugin-vue-components/vite'
import { AntDesignVueResolver } from 'unplugin-vue-components/resolvers'

export default defineConfig({
  base: '/',
  build: {
    outDir: 'dist',
    rollupOptions: {
      output: {
        // 第三方依赖拆为 vendor chunk，改业务代码不使缓存失效
        manualChunks(id) {
          if (id.includes('node_modules')) {
            if (id.includes('ant-design-vue') || id.includes('@ant-design')) return 'antd'
            if (id.includes('dayjs')) return 'dayjs'
            return 'vendor'
          }
        },
      },
    },
  },
  plugins: [
    vue(),
    Components({
      // antdv 4 是 CSS-in-JS，无独立 css 文件，用 'css-in-js' 引 es/xxx/style 入口
      resolvers: [AntDesignVueResolver({ importStyle: 'css-in-js' })],
    }),
  ],
  server: {
    proxy: { '/api': 'http://localhost:8080' },
  },
})
