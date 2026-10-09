import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'
import Components from 'unplugin-vue-components/vite'
import { AntDesignVueResolver } from 'unplugin-vue-components/resolvers'

export default defineConfig({
  base: '/',
  build: { outDir: 'dist' },
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
