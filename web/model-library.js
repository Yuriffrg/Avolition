const collator = new Intl.Collator(undefined, {numeric:true,sensitivity:'base'});
const parts = value => String(value || '').split(/[\\/]+/).filter(Boolean);
const stem = value => String(value || '').replace(/\.(?:owentity|owmdl)$/i,'');
export const modelPathKey = value => String(value || '').replaceAll('\\','/').toLocaleLowerCase('en-US');

export function describeModelEntry(entry) {
  if (!entry || typeof entry.path !== 'string' || !/\.(?:owentity|owmdl)$/i.test(entry.path)) return null;
  const pathParts=parts(entry.path), relative=parts(entry.relativePath), lower=pathParts.map(value=>value.toLowerCase());
  const heroIndex=lower.lastIndexOf('heroes'), skinIndex=lower.lastIndexOf('skin');
  const hero=(typeof entry.hero==='string'&&entry.hero.trim()) || (heroIndex>=0 ? pathParts[heroIndex+1] : skinIndex>0 ? pathParts[skinIndex-1] : '') || 'Other assets';
  const afterSkin=skinIndex>=0 ? pathParts.slice(skinIndex+1,-1) : [];
  const boundary=afterSkin.findIndex(value=>/^(?:entities|models)$/i.test(value));
  const skin=(typeof entry.skin==='string'&&entry.skin.trim()) || (boundary>0 ? afterSkin[boundary-1] : '') || 'Unsorted';
  const name=stem(pathParts.at(-1)), kind=/\.owentity$/i.test(entry.path)?'entity':'model';
  const gameplay=/^Gameplay(1|3)P$/i.exec(name);
  const rank=kind==='entity' && gameplay ? gameplay[1]==='1'?0:1 : kind==='entity'?2:3;
  const guid=/^[\da-f]{8,16}(?:\.[\da-f]{3})?$/i.test(name) ? name.toUpperCase() : '';
  const label=guid ? `${kind==='entity'?'Entity':'Model'} ${guid}` : name || entry.name || 'Unnamed model';
  const boneCount=Number.isInteger(entry.boneCount)&&entry.boneCount>=0?entry.boneCount:null;
  const boneCountError=typeof entry.boneCountError==='string'?entry.boneCountError:'';
  const size=Number.isSafeInteger(entry.size)&&entry.size>=0?entry.size:null;
  const sizeError=typeof entry.sizeError==='string'?entry.sizeError:'';
  return {entry,path:entry.path,key:modelPathKey(entry.path),hero,skin,kind,rank,guid,label,boneCount,boneCountError,size,sizeError,
    context:`${hero} · ${skin}`,detail:kind==='entity'?'Entity':'Model',
    search:[hero,skin,label,entry.name,entry.path,relative.join(' ')].filter(Boolean).join(' ').toLocaleLowerCase()};
}

export function compareModelEntries(a,b,sort='gameplay') {
  const field={'joints-desc':'boneCount','joints-asc':'boneCount','size-desc':'size','size-asc':'size'}[sort];
  if(field){
    // Unreadable metadata belongs last in either direction; zero is valid.
    const left=a[field],right=b[field];
    if(left===null&&right!==null)return 1;
    if(right===null&&left!==null)return -1;
    if(left!==null&&right!==null&&left!==right)return (sort.endsWith('-desc')?-1:1)*(left-right);
  }
  return a.rank-b.rank || collator.compare(a.hero,b.hero) || collator.compare(a.skin,b.skin)
    || collator.compare(a.label,b.label) || collator.compare(a.path,b.path);
}

export function sortModelEntries(entries=[]) {
  return (Array.isArray(entries)?entries:[]).map(describeModelEntry).filter(Boolean).sort(compareModelEntries).map(item=>item.entry);
}

export function modelBrowserGroups(entries, {query='',hero='',skin='',kind='',sort='gameplay'}={}) {
  const tokens=query.trim().toLocaleLowerCase().split(/\s+/).filter(Boolean), groups=new Map();
  const found=entries.filter(item=>(!hero||item.hero===hero)&&(!skin||item.skin===skin)&&(!kind||item.kind===kind)
    && tokens.every(token=>item.search.includes(token))).sort((a,b)=>compareModelEntries(a,b,sort));
  for(const item of found) {
    if(!groups.has(item.hero))groups.set(item.hero,{key:`hero:${item.hero}`,label:item.hero,kind:'hero',count:0,children:new Map()});
    const group=groups.get(item.hero);group.count++;
    if(!group.children.has(item.skin))group.children.set(item.skin,{key:`skin:${JSON.stringify([item.hero,item.skin])}`,label:item.skin,kind:'skin',count:0,children:[]});
    const skinGroup=group.children.get(item.skin);skinGroup.count++;
    skinGroup.children.push({...item,kind:'asset',assetKind:item.kind});
  }
  return [...groups.values()].sort((a,b)=>collator.compare(a.label,b.label)).map(group=>({...group,
    children:[...group.children.values()].sort((a,b)=>collator.compare(a.label,b.label))}));
}
