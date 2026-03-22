function main () {
  let name = 'Nyan8'
  if (typeof nyanAllParams.name !== 'undefined') {
    name = nyanAllParams.name
  }

  let log = nyanCallMe({ api: 'hello2' })
  console.log(log)

  return JSON.stringify({
    success: true,
    status: 200,
    data: {
      message: 'hello! ' + name
    }
  })
}

main()
