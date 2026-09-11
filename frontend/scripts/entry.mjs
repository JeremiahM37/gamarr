import {readFile,writeFile} from 'node:fs/promises';
import {createHash} from 'node:crypto';
const entry=new URL('../../web/index.html',import.meta.url);
let html=await readFile(entry,'utf8');
for(const name of ['gamarr-react.js','gamarr-react.css']){
 const bytes=await readFile(new URL(`../../web/static/${name}`,import.meta.url));
 const hash=createHash('sha256').update(bytes).digest('hex').slice(0,16);
 const pattern=new RegExp(`/static/${name.replace('.', '\\.')}[^"']*`,'g');
 html=html.replace(pattern,`/static/${name}?v=${hash}`);
}
await writeFile(entry,html);
